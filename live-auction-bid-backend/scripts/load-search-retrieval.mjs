#!/usr/bin/env node

import { mkdir, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const defaultQueries = [
  '预算500以内适合送礼的翡翠手镯',
  '正在竞拍的玛瑙吊坠',
  '1000元以内的收藏品',
  '低价玉石项链',
  '直播中的珠宝拍品',
];

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch((error) => {
    console.error(error?.stack || error);
    process.exit(1);
  });
}

async function main() {
  const config = readConfig(process.env);
  if (!config.confirmed) {
    throw new Error('refusing search load without CONFIRM_SEARCH_LOAD=1');
  }
  if (config.aiModeEvidence !== 'mock') {
    throw new Error('SEARCH_LOAD_AI_MODE=mock is required so external LLM latency and cost do not contaminate the retrieval gate');
  }

  await verifyMetricsEndpoint(config.metricsUrl, config.aiModeEvidence);
  await runRequests(config, config.warmupRequests, 'warmup');
  const metricsBefore = await scrapeMetrics(config.metricsUrl);
  const measured = await runRequests(config, config.requests, 'measured');
  const metricsAfter = await scrapeMetrics(config.metricsUrl);

  const latencies = measured
    .filter((item) => !item.error)
    .map((item) => item.durationMs)
    .sort((left, right) => left - right);
  const errors = measured.filter((item) => item.error);
  const retrieval = retrievalReport(metricsBefore, metricsAfter, config.requiredSources, config.retrievalP99LimitMs);
  const apiP99Ms = percentile(latencies, 99);
  const errorRate = measured.length ? errors.length / measured.length : 1;
  const failures = [...retrieval.failures];
  if (processStartChanged(metricsBefore, metricsAfter)) {
    failures.push('Gateway process_start_time_seconds changed during the measured window');
  }
  if (errorRate > config.maxErrorRate) {
    failures.push(`API error rate ${errorRate} exceeds ${config.maxErrorRate}`);
  }
  if (apiP99Ms > config.apiP99LimitMs) {
    failures.push(`API p99 ${apiP99Ms}ms exceeds ${config.apiP99LimitMs}ms mock-AI ceiling`);
  }

  const report = {
    schemaVersion: 1,
    status: failures.length ? 'FAIL' : 'PASS',
    generatedAt: new Date().toISOString(),
    baseUrl: config.baseUrl,
    metricsUrl: config.metricsUrl,
    aiModeEvidence: config.aiModeEvidence,
    requests: config.requests,
    concurrency: config.concurrency,
    warmupRequests: config.warmupRequests,
    succeeded: measured.length - errors.length,
    errors: errors.length,
    errorRate,
    apiLatencyMs: {
      p50: percentile(latencies, 50),
      p95: percentile(latencies, 95),
      p99: apiP99Ms,
      limitP99: config.apiP99LimitMs,
    },
    retrieval,
    failures,
  };
  const output = `${JSON.stringify(report, null, 2)}\n`;
  if (config.reportFile) {
    await mkdir(dirname(config.reportFile), { recursive: true });
    await writeFile(config.reportFile, output, 'utf8');
  }
  console.log(output.trimEnd());
  if (failures.length) throw new Error(`search retrieval gate failed: ${failures.join('; ')}`);
}

function readConfig(env) {
  const baseUrl = String(env.BASE_URL || 'http://127.0.0.1:18080').trim().replace(/\/+$/, '');
  const metricsUrl = String(env.GATEWAY_METRICS_URL || `${baseUrl}/metrics`).trim().replace(/\/+$/, '');
  const queries = String(env.SEARCH_QUERIES || '')
    .split('|')
    .map((value) => value.trim())
    .filter(Boolean);
  const requiredSources = String(env.REQUIRED_SEARCH_SOURCES || 'elasticsearch,pgvector')
    .split(',')
    .map((value) => value.trim())
    .filter(Boolean);
  if (!requiredSources.length) throw new Error('REQUIRED_SEARCH_SOURCES must not be empty');
  return {
    baseUrl,
    metricsUrl,
    requests: parsePositiveInt(env.REQUESTS, 100),
    concurrency: parsePositiveInt(env.CONCURRENCY, 20),
    warmupRequests: parseNonNegativeInt(env.WARMUP_REQUESTS, 10),
    requestTimeoutMs: parsePositiveInt(env.REQUEST_TIMEOUT_MS, 15_000),
    retrievalP99LimitMs: parsePositiveInt(env.RETRIEVAL_P99_LIMIT_MS, 1_000),
    apiP99LimitMs: parsePositiveInt(env.API_P99_LIMIT_MS, 3_000),
    maxErrorRate: parseRate(env.MAX_ERROR_RATE, 0),
    queries: queries.length ? queries : defaultQueries,
    requiredSources,
    accessToken: String(env.ACCESS_TOKEN || '').trim(),
    reportFile: env.REPORT_FILE ? resolve(env.REPORT_FILE) : '',
    aiModeEvidence: String(env.SEARCH_LOAD_AI_MODE || '').trim(),
    confirmed: env.CONFIRM_SEARCH_LOAD === '1',
  };
}

async function runRequests(config, count, phase) {
  if (count === 0) return [];
  const jobs = Array.from({ length: count }, (_, index) => ({
    index,
    query: config.queries[index % config.queries.length],
  }));
  return runPool(jobs, config.concurrency, async (job) => {
    const startedAt = performance.now();
    try {
      await consultBuyer(config, job.query);
      return { ...job, phase, durationMs: Math.round(performance.now() - startedAt) };
    } catch (error) {
      return {
        ...job,
        phase,
        durationMs: Math.round(performance.now() - startedAt),
        error: error instanceof Error ? error.message : String(error),
      };
    }
  });
}

async function runPool(items, concurrency, task) {
  const results = new Array(items.length);
  let cursor = 0;
  const workers = Array.from({ length: Math.min(concurrency, items.length) }, async () => {
    while (cursor < items.length) {
      const index = cursor;
      cursor += 1;
      results[index] = await task(items[index]);
    }
  });
  await Promise.all(workers);
  return results;
}

async function consultBuyer(config, query) {
  const headers = new Headers({ Accept: 'application/json', 'Content-Type': 'application/json' });
  if (config.accessToken) headers.set('Authorization', `Bearer ${config.accessToken}`);
  let response;
  try {
    response = await fetch(`${config.baseUrl}/api/ai/buyer/consult`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ query }),
      signal: AbortSignal.timeout(config.requestTimeoutMs),
    });
  } catch (error) {
    throw new Error(`consult request failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  const text = await response.text();
  let body = {};
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(`consult returned non-JSON HTTP ${response.status}: ${text.slice(0, 160)}`);
  }
  if (!response.ok) throw new Error(`consult HTTP ${response.status}: ${text.slice(0, 240)}`);
  if (body.result && Number(body.result.code || 0) !== 0) {
    throw new Error(`consult business error ${body.result.code}: ${body.result.message || 'unknown'}`);
  }
  return body;
}

async function verifyMetricsEndpoint(url, requiredAIMode) {
  const metrics = await scrapeMetrics(url);
  if (!parseMetricSamples(metrics, 'go_build_info').length) {
    throw new Error(`Gateway metrics endpoint ${url} does not expose go_build_info`);
  }
  const activeModes = parseMetricSamples(metrics, 'auction_ai_assistant_info')
    .filter((sample) => sample.value === 1)
    .map((sample) => sample.labels.mode);
  if (activeModes.length !== 1 || activeModes[0] !== requiredAIMode) {
    throw new Error(`Gateway AI mode is ${activeModes.join(',') || 'unreported'}, required ${requiredAIMode}`);
  }
}

async function scrapeMetrics(url) {
  let response;
  try {
    response = await fetch(url, { signal: AbortSignal.timeout(5_000) });
  } catch (error) {
    throw new Error(`GET metrics ${url} failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (!response.ok) throw new Error(`GET metrics ${url} HTTP ${response.status}`);
  return response.text();
}

function retrievalReport(beforeText, afterText, requiredSources, p99LimitMs) {
  const bucketFamily = 'auction_search_retrieval_duration_ms_bucket';
  const counterFamily = 'auction_search_retrieval_total';
  const beforeBuckets = parseMetricSamples(beforeText, bucketFamily);
  const afterBuckets = parseMetricSamples(afterText, bucketFamily);
  const beforeCounters = parseMetricSamples(beforeText, counterFamily);
  const afterCounters = parseMetricSamples(afterText, counterFamily);
  const sources = {};
  const failures = [];
  for (const source of requiredSources) {
    const buckets = histogramDelta(beforeBuckets, afterBuckets, { source, result: 'ok' });
    const sampleCount = buckets.get('+Inf') || 0;
    const p99Ms = histogramPercentileUpperBound(buckets, 99);
    const errorCount = counterDelta(beforeCounters, afterCounters, { source, result: 'error' });
    const counterReset = samplesReset(beforeBuckets, afterBuckets, { source, result: 'ok' }, 'le') ||
      samplesReset(beforeCounters, afterCounters, { source }, 'result');
    sources[source] = { sampleCount, errorCount, p99Ms, limitP99Ms: p99LimitMs, counterReset };
    if (counterReset) failures.push(`${source} Prometheus counters reset during the measured window`);
    if (sampleCount <= 0) failures.push(`${source} produced no successful indexed retrieval samples`);
    if (errorCount > 0) failures.push(`${source} retrieval errors increased by ${errorCount}`);
    if (!Number.isFinite(p99Ms)) failures.push(`${source} p99 overflowed the largest finite histogram bucket`);
    else if (p99Ms > p99LimitMs) failures.push(`${source} retrieval p99 ${p99Ms}ms exceeds ${p99LimitMs}ms`);
  }
  return { p99LimitMs, requiredSources, sources, failures };
}

function parseMetricSamples(text, family) {
  const samples = [];
  for (const rawLine of String(text || '').split('\n')) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;
    const match = line.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+([^\s]+)(?:\s+\d+)?$/);
    if (!match || match[1] !== family) continue;
    const value = Number(match[3]);
    if (!Number.isFinite(value)) continue;
    samples.push({ labels: parseMetricLabels(match[2] || ''), value });
  }
  return samples;
}

function parseMetricLabels(raw) {
  if (!raw.startsWith('{')) return {};
  const labels = {};
  const pattern = /([a-zA-Z_][a-zA-Z0-9_]*)="((?:\\.|[^"\\])*)"/g;
  for (const match of raw.matchAll(pattern)) {
    labels[match[1]] = match[2].replace(/\\n/g, '\n').replace(/\\"/g, '"').replace(/\\\\/g, '\\');
  }
  return labels;
}

function histogramDelta(beforeSamples, afterSamples, requiredLabels) {
  const before = samplesByLabel(beforeSamples, requiredLabels, 'le');
  const after = samplesByLabel(afterSamples, requiredLabels, 'le');
  const result = new Map();
  for (const [upperBound, afterValue] of after) {
    result.set(upperBound, counterIncrease(before.get(upperBound) || 0, afterValue));
  }
  return result;
}

function counterDelta(beforeSamples, afterSamples, requiredLabels) {
  const before = matchingSampleTotal(beforeSamples, requiredLabels);
  const after = matchingSampleTotal(afterSamples, requiredLabels);
  return counterIncrease(before, after);
}

function samplesByLabel(samples, requiredLabels, keyLabel) {
  const result = new Map();
  for (const sample of samples) {
    if (!labelsMatch(sample.labels, requiredLabels)) continue;
    const key = sample.labels[keyLabel];
    if (key === undefined) continue;
    result.set(key, (result.get(key) || 0) + sample.value);
  }
  return result;
}

function matchingSampleTotal(samples, requiredLabels) {
  return samples.reduce((total, sample) => (
    labelsMatch(sample.labels, requiredLabels) ? total + sample.value : total
  ), 0);
}

function labelsMatch(labels, requiredLabels) {
  return Object.entries(requiredLabels).every(([key, value]) => labels[key] === value);
}

function counterIncrease(before, after) {
  return after >= before ? after - before : after;
}

function samplesReset(beforeSamples, afterSamples, requiredLabels, keyLabel) {
  const before = samplesByLabel(beforeSamples, requiredLabels, keyLabel);
  const after = samplesByLabel(afterSamples, requiredLabels, keyLabel);
  for (const [key, beforeValue] of before) {
    const afterValue = after.get(key);
    if (afterValue !== undefined && afterValue < beforeValue) return true;
  }
  return false;
}

function processStartChanged(beforeText, afterText) {
  const before = parseMetricSamples(beforeText, 'process_start_time_seconds');
  const after = parseMetricSamples(afterText, 'process_start_time_seconds');
  if (before.length !== 1 || after.length !== 1) return true;
  return before[0].value !== after[0].value;
}

function histogramPercentileUpperBound(buckets, percentileValue) {
  const total = buckets.get('+Inf') || 0;
  if (total <= 0) return 0;
  const target = Math.ceil((percentileValue / 100) * total);
  const ordered = [...buckets.entries()].sort(([left], [right]) => {
    if (left === '+Inf') return 1;
    if (right === '+Inf') return -1;
    return Number(left) - Number(right);
  });
  for (const [upperBound, count] of ordered) {
    if (count >= target) return upperBound === '+Inf' ? Number.POSITIVE_INFINITY : Number(upperBound);
  }
  return Number.POSITIVE_INFINITY;
}

function percentile(values, percentileValue) {
  if (!values.length) return 0;
  const index = Math.min(values.length - 1, Math.ceil((percentileValue / 100) * values.length) - 1);
  return values[index];
}

function parsePositiveInt(raw, fallback) {
  const value = Number.parseInt(String(raw || ''), 10);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function parseNonNegativeInt(raw, fallback) {
  const value = Number.parseInt(String(raw || ''), 10);
  return Number.isFinite(value) && value >= 0 ? value : fallback;
}

function parseRate(raw, fallback) {
  const value = Number.parseFloat(String(raw || ''));
  return Number.isFinite(value) && value >= 0 && value <= 1 ? value : fallback;
}

export {
  counterDelta,
  histogramDelta,
  histogramPercentileUpperBound,
  parseMetricLabels,
  parseMetricSamples,
  percentile,
  processStartChanged,
  readConfig,
  retrievalReport,
  runPool,
  samplesReset,
};
