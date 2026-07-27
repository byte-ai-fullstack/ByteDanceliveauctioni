import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  histogramDelta,
  histogramPercentileUpperBound,
  parseMetricLabels,
  parseMetricSamples,
  processStartChanged,
  readConfig,
  retrievalReport,
  runPool,
} from './load-search-retrieval.mjs';

test('metric parser keeps exact families and unescapes bounded labels', () => {
  const metrics = [
    '# HELP auction_search_retrieval_total retrievals',
    'auction_search_retrieval_total{source="elasticsearch",result="ok"} 12',
    'auction_search_retrieval_total_extra{source="elasticsearch"} 999',
    'go_build_info{version="go1.25.0"} 1',
  ].join('\n');

  assert.deepEqual(parseMetricSamples(metrics, 'auction_search_retrieval_total'), [
    { labels: { source: 'elasticsearch', result: 'ok' }, value: 12 },
  ]);
  assert.deepEqual(parseMetricLabels('{source="pgvector",detail="escaped\\\\value"}'), {
    source: 'pgvector',
    detail: 'escaped\\value',
  });
});

test('histogram delta handles process counter reset and returns conservative upper bound', () => {
  const before = parseMetricSamples([
    'auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="500"} 90',
    'auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="1000"} 100',
    'auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="+Inf"} 100',
  ].join('\n'), 'auction_search_retrieval_duration_ms_bucket');
  const after = parseMetricSamples([
    'auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="500"} 9',
    'auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="1000"} 10',
    'auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="+Inf"} 10',
  ].join('\n'), 'auction_search_retrieval_duration_ms_bucket');

  const delta = histogramDelta(before, after, { source: 'pgvector', result: 'ok' });
  assert.equal(delta.get('+Inf'), 10);
  assert.equal(histogramPercentileUpperBound(delta, 99), 1000);
});

test('retrieval report rejects missing, failed, slow and overflowed sources', () => {
  const before = [
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="500"} 1',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="2500"} 1',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="+Inf"} 1',
    'auction_search_retrieval_total{source="elasticsearch",result="error"} 0',
  ].join('\n');
  const after = [
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="500"} 1',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="2500"} 3',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="+Inf"} 3',
    'auction_search_retrieval_total{source="elasticsearch",result="error"} 1',
  ].join('\n');

  const report = retrievalReport(before, after, ['elasticsearch', 'pgvector'], 1000);
  assert.equal(report.sources.elasticsearch.sampleCount, 2);
  assert.equal(report.sources.elasticsearch.p99Ms, 2500);
  assert.equal(report.sources.elasticsearch.errorCount, 1);
  assert.equal(report.sources.elasticsearch.counterReset, false);
  assert.equal(report.sources.pgvector.sampleCount, 0);
  assert.match(report.failures.join('; '), /elasticsearch retrieval errors increased/);
  assert.match(report.failures.join('; '), /elasticsearch retrieval p99 2500ms exceeds 1000ms/);
  assert.match(report.failures.join('; '), /pgvector produced no successful indexed retrieval samples/);
});

test('retrieval report and process identity reject reset or restarted evidence windows', () => {
  const before = [
    'process_start_time_seconds 100',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="1000"} 20',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="+Inf"} 20',
    'auction_search_retrieval_total{source="elasticsearch",result="ok"} 20',
  ].join('\n');
  const after = [
    'process_start_time_seconds 200',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="1000"} 2',
    'auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="+Inf"} 2',
    'auction_search_retrieval_total{source="elasticsearch",result="ok"} 2',
  ].join('\n');

  const report = retrievalReport(before, after, ['elasticsearch'], 1000);
  assert.equal(report.sources.elasticsearch.counterReset, true);
  assert.match(report.failures.join('; '), /Prometheus counters reset/);
  assert.equal(processStartChanged(before, after), true);
  assert.equal(processStartChanged('process_start_time_seconds 100', 'process_start_time_seconds 100'), false);
});

test('config has explicit safe defaults and accepts bounded overrides', () => {
  const config = readConfig({
    BASE_URL: 'http://gateway:18080/',
    CONFIRM_SEARCH_LOAD: '1',
    REQUESTS: '20',
    CONCURRENCY: '4',
    WARMUP_REQUESTS: '0',
    MAX_ERROR_RATE: '0.01',
    REQUIRED_SEARCH_SOURCES: 'elasticsearch,pgvector',
    SEARCH_LOAD_AI_MODE: 'mock',
  });

  assert.equal(config.baseUrl, 'http://gateway:18080');
  assert.equal(config.metricsUrl, 'http://gateway:18080/metrics');
  assert.equal(config.requests, 20);
  assert.equal(config.concurrency, 4);
  assert.equal(config.warmupRequests, 0);
  assert.equal(config.retrievalP99LimitMs, 1000);
  assert.equal(config.maxErrorRate, 0.01);
  assert.equal(config.aiModeEvidence, 'mock');
  assert.equal(config.confirmed, true);
});

test('bounded worker pool preserves output order', async () => {
  const values = await runPool([3, 1, 2], 2, async (value) => {
    await new Promise((resolve) => setTimeout(resolve, value));
    return value * 10;
  });
  assert.deepEqual(values, [30, 10, 20]);
});

test('CLI verifies live mock mode and gates a measured metrics delta', async (t) => {
  let consultCount = 0;
  const server = createServer(async (request, response) => {
    if (request.url === '/metrics') {
      response.writeHead(200, { 'Content-Type': 'text/plain' });
      response.end([
        'go_build_info{version="go1.25.0"} 1',
        'process_start_time_seconds 100',
        'auction_ai_assistant_info{mode="mock"} 1',
        'auction_ai_assistant_info{mode="external"} 0',
        `auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="500"} ${consultCount}`,
        `auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="1000"} ${consultCount}`,
        `auction_search_retrieval_duration_ms_bucket{source="elasticsearch",result="ok",le="+Inf"} ${consultCount}`,
        `auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="500"} ${consultCount}`,
        `auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="1000"} ${consultCount}`,
        `auction_search_retrieval_duration_ms_bucket{source="pgvector",result="ok",le="+Inf"} ${consultCount}`,
        'auction_search_retrieval_total{source="elasticsearch",result="error"} 0',
        'auction_search_retrieval_total{source="pgvector",result="error"} 0',
      ].join('\n'));
      return;
    }
    if (request.method === 'POST' && request.url === '/api/ai/buyer/consult') {
      for await (const _chunk of request) { // Drain the request before responding.
        // Intentionally empty.
      }
      consultCount += 1;
      response.writeHead(200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify({ result: { code: 0 }, results: [] }));
      return;
    }
    response.writeHead(404).end();
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));

  const scratch = await mkdtemp(join(tmpdir(), 'auction-search-load-'));
  t.after(() => rm(scratch, { recursive: true, force: true }));
  const address = server.address();
  const baseUrl = `http://127.0.0.1:${address.port}`;
  const reportFile = join(scratch, 'report.json');
  const script = join(dirname(fileURLToPath(import.meta.url)), 'load-search-retrieval.mjs');
  const child = spawn(process.execPath, [script], {
    env: {
      ...process.env,
      CONFIRM_SEARCH_LOAD: '1',
      SEARCH_LOAD_AI_MODE: 'mock',
      BASE_URL: baseUrl,
      GATEWAY_METRICS_URL: `${baseUrl}/metrics`,
      REQUESTS: '2',
      WARMUP_REQUESTS: '1',
      CONCURRENCY: '1',
      REPORT_FILE: reportFile,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const stdout = [];
  const stderr = [];
  child.stdout.on('data', (chunk) => stdout.push(chunk));
  child.stderr.on('data', (chunk) => stderr.push(chunk));
  const exitCode = await new Promise((resolve) => child.once('close', resolve));
  assert.equal(exitCode, 0, `${Buffer.concat(stderr).toString()}\n${Buffer.concat(stdout).toString()}`);

  const report = JSON.parse(await readFile(reportFile, 'utf8'));
  assert.equal(report.status, 'PASS');
  assert.equal(report.requests, 2);
  assert.equal(report.retrieval.sources.elasticsearch.sampleCount, 2);
  assert.equal(report.retrieval.sources.pgvector.sampleCount, 2);
  assert.equal(report.retrieval.sources.elasticsearch.p99Ms, 500);
  assert.deepEqual(report.failures, []);
});
