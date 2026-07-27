#!/usr/bin/env node

import { spawn } from 'node:child_process';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const activeChildren = new Set();

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(async (error) => {
    for (const child of activeChildren) child.kill('SIGTERM');
    console.error(error?.stack || error);
    process.exit(1);
  });
}

async function main() {
  const config = readFleetConfig(process.env);
  if (!config.confirmed) throw new Error('refusing fleet load without CONFIRM_BID_LOAD=1');
  await mkdir(config.resultsRoot, { recursive: true });
  await mkdir(config.runDir);
  await mkdir(dirname(config.reportFile), { recursive: true });

  const commonStartFile = join(config.runDir, 'barrier-start.json');
  const children = Array.from({ length: config.scenarios }, (_, index) => {
    const scenario = index + 1;
    return spawnScenario(config, scenario, commonStartFile);
  });
  await waitForReady(children, config.readyTimeoutMs);
  const startAtUnixMs = Date.now() + config.startDelayMs;
  await writeFile(commonStartFile, `${JSON.stringify({ startAtUnixMs })}\n`, { encoding: 'utf8', flag: 'wx' });

  const exits = await Promise.all(children.map((child) => child.completion));
  for (const exit of exits) {
    await writeFile(exit.stdoutFile, exit.stdout, 'utf8');
    await writeFile(exit.stderrFile, exit.stderr, 'utf8');
  }
  const failedChildren = exits.filter((exit) => exit.exitCode !== 0);
  if (failedChildren.length) {
    throw new Error(`fleet child failures: ${failedChildren.map((item) => `${item.scenario}:${item.exitCode}`).join(', ')}`);
  }

  const reports = await Promise.all(children.map((child) => readJSON(child.reportFile)));
  const evaluation = evaluateFleetReports(reports, config);
  const report = {
    schemaVersion: 1,
    status: evaluation.failures.length ? 'FAIL' : 'PASS',
    generatedAt: new Date().toISOString(),
    runId: config.runId,
    scenarios: config.scenarios,
    ratePerScenario: config.ratePerScenario,
    durationSeconds: config.durationSeconds,
    biddersPerScenario: config.biddersPerScenario,
    ...evaluation,
    childReports: children.map((child) => child.reportFile),
  };
  const output = `${JSON.stringify(report, null, 2)}\n`;
  await writeFile(config.reportFile, output, 'utf8');
  console.log(output.trimEnd());
  if (evaluation.failures.length) throw new Error(`fleet load gate failed: ${evaluation.failures.join('; ')}`);
}

function readFleetConfig(env) {
  const runId = String(env.RUN_ID || `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`).trim();
  if (!/^[a-z0-9][a-z0-9_-]*$/.test(runId) || runId.length > 32) {
    throw new Error('RUN_ID must be 1-32 lowercase letters, digits, underscores or hyphens');
  }
  const resultsRoot = resolve(env.RESULTS_DIR || 'test-results/bid-fleet');
  const runDir = join(resultsRoot, runId);
  const scenarios = parsePositiveInt(env.SCENARIOS, 2);
  const ratePerScenario = parsePositiveNumber(env.RATE_PER_SCENARIO, 105);
  return {
    runId,
    resultsRoot,
    runDir,
    reportFile: resolve(env.REPORT_FILE || join(runDir, 'fleet-report.json')),
    scenarios,
    ratePerScenario,
    durationSeconds: parsePositiveNumber(env.LOAD_DURATION_SECONDS, 60),
    biddersPerScenario: parsePositiveInt(env.BIDDERS_PER_SCENARIO, 100),
    bidP99LimitMs: parsePositiveNumber(env.BID_P99_LIMIT_MS, 200),
    maxSystemErrorRate: parseRate(env.MAX_SYSTEM_ERROR_RATE, 0),
    minAggregateOfferedRate: parsePositiveNumber(env.MIN_AGGREGATE_OFFERED_RATE, 200),
    minOverlapRatio: parseRate(env.MIN_OVERLAP_RATIO, 0.95),
    readyTimeoutMs: parsePositiveInt(env.FLEET_READY_TIMEOUT_MS, 10 * 60_000),
    startDelayMs: parsePositiveInt(env.FLEET_START_DELAY_MS, 2_000),
    confirmed: env.CONFIRM_BID_LOAD === '1',
  };
}

function spawnScenario(config, scenario, commonStartFile) {
  const script = join(dirname(fileURLToPath(import.meta.url)), 'load-bid-hot-path.mjs');
  const readyFile = join(config.runDir, `scenario-${scenario}.ready.json`);
  const reportFile = join(config.runDir, `scenario-${scenario}.report.json`);
  const stdoutFile = join(config.runDir, `scenario-${scenario}.stdout.log`);
  const stderrFile = join(config.runDir, `scenario-${scenario}.stderr.log`);
  const child = spawn(process.execPath, [script], {
    env: {
      ...process.env,
      RUN_ID: `${config.runId}-s${scenario}`,
      MERCHANT_USERNAME: `load_fleet_${config.runId}_s${scenario}`,
      BUYER_PREFIX: `load_fleet_buyer_${config.runId}_s${scenario}`,
      CONCURRENCY: String(config.biddersPerScenario),
      TARGET_BID_RATE_PER_SECOND: String(config.ratePerScenario),
      LOAD_DURATION_SECONDS: String(config.durationSeconds),
      BID_P99_LIMIT_MS: String(config.bidP99LimitMs),
      MIN_BID_THROUGHPUT_PER_SECOND: String(config.ratePerScenario * 0.95),
      MAX_SYSTEM_ERROR_RATE: String(config.maxSystemErrorRate),
      MIN_OFFERED_RATE_RATIO: '0.99',
      MAX_SCHEDULE_DRIFT_P99_MS: '100',
      WS_CONNECTIONS: '0',
      REPORT_FILE: reportFile,
      LOAD_BARRIER_READY_FILE: readyFile,
      LOAD_BARRIER_START_FILE: commonStartFile,
      LOAD_BARRIER_TIMEOUT_MS: String(config.readyTimeoutMs + config.startDelayMs + 5_000),
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  activeChildren.add(child);
  const stdout = [];
  const stderr = [];
  child.stdout.on('data', (chunk) => stdout.push(chunk));
  child.stderr.on('data', (chunk) => stderr.push(chunk));
  const state = { exited: false, exitCode: null };
  const completion = new Promise((resolveCompletion) => {
    child.once('close', (exitCode) => {
      activeChildren.delete(child);
      state.exited = true;
      state.exitCode = exitCode;
      resolveCompletion({
        scenario,
        exitCode,
        stdout: Buffer.concat(stdout).toString(),
        stderr: Buffer.concat(stderr).toString(),
        stdoutFile,
        stderrFile,
      });
    });
  });
  return { scenario, child, state, completion, readyFile, reportFile };
}

async function waitForReady(children, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  const pending = new Set(children.map((child) => child.scenario));
  while (Date.now() < deadline) {
    for (const child of children) {
      if (!pending.has(child.scenario)) continue;
      if (child.state.exited) {
        throw new Error(`scenario ${child.scenario} exited with ${child.state.exitCode} before load barrier`);
      }
      try {
        await readFile(child.readyFile, 'utf8');
        pending.delete(child.scenario);
      } catch (error) {
        if (error?.code !== 'ENOENT') throw error;
      }
    }
    if (!pending.size) return;
    await delay(100);
  }
  throw new Error(`fleet readiness timed out after ${timeoutMs}ms; pending=${[...pending].join(',')}`);
}

function evaluateFleetReports(reports, config) {
  const failures = [];
  if (reports.length !== config.scenarios) failures.push(`received ${reports.length}/${config.scenarios} child reports`);
  for (const [index, report] of reports.entries()) {
    if (report.status !== 'PASS') failures.push(`scenario ${index + 1} status is ${report.status || 'missing'}`);
  }
  const starts = reports.map((report) => Number(report.bidBatchStartedAtUnixMs)).filter(Number.isFinite);
  const ends = reports.map((report) => Number(report.bidBatchEndedAtUnixMs)).filter(Number.isFinite);
  const overlapStartUnixMs = starts.length ? Math.max(...starts) : 0;
  const overlapEndUnixMs = ends.length ? Math.min(...ends) : 0;
  const overlapMs = Math.max(0, overlapEndUnixMs - overlapStartUnixMs);
  const expectedWindowMs = config.durationSeconds * 1000;
  const overlapRatio = expectedWindowMs > 0 ? overlapMs / expectedWindowMs : 0;
  if (overlapRatio < config.minOverlapRatio) {
    failures.push(`steady-window overlap ratio ${round(overlapRatio, 4)} is below ${config.minOverlapRatio}`);
  }
  const aggregateOfferedRatePerSecond = reports.reduce(
    (total, report) => total + Number(report.loadProfile?.offeredRatePerSecond || 0),
    0,
  );
  if (aggregateOfferedRatePerSecond < config.minAggregateOfferedRate) {
    failures.push(`aggregate offered rate ${round(aggregateOfferedRatePerSecond, 2)}/s is below ${config.minAggregateOfferedRate}/s`);
  }
  const totalRequests = reports.reduce((total, report) => total + Number(report.total || 0), 0);
  const totalSystemErrors = reports.reduce((total, report) => total + Number(report.systemErrors || 0), 0);
  const errorRate = totalRequests ? totalSystemErrors / totalRequests : 1;
  if (errorRate > config.maxSystemErrorRate) {
    failures.push(`aggregate system error rate ${round(errorRate, 6)} exceeds ${config.maxSystemErrorRate}`);
  }
  const aggregateP99UpperBoundMs = reports.reduce((maximum, report) => Math.max(maximum, Number(report.p99Ms || 0)), 0);
  if (aggregateP99UpperBoundMs >= config.bidP99LimitMs) {
    failures.push(`aggregate p99 upper bound ${aggregateP99UpperBoundMs}ms must be below ${config.bidP99LimitMs}ms`);
  }
  const wallStart = starts.length ? Math.min(...starts) : 0;
  const wallEnd = ends.length ? Math.max(...ends) : 0;
  const wallDurationMs = Math.max(0, wallEnd - wallStart);
  return {
    totalRequests,
    totalSystemErrors,
    errorRate,
    aggregateP99UpperBoundMs,
    aggregateOfferedRatePerSecond: round(aggregateOfferedRatePerSecond, 2),
    overlapStartUnixMs,
    overlapEndUnixMs,
    overlapMs,
    overlapRatio: round(overlapRatio, 4),
    wallDurationMs,
    completionThroughputPerSecond: wallDurationMs > 0 ? round(totalRequests / (wallDurationMs / 1000), 2) : 0,
    failures,
  };
}

async function readJSON(path) {
  return JSON.parse(await readFile(path, 'utf8'));
}

function parsePositiveInt(raw, fallback) {
  const value = Number.parseInt(String(raw || ''), 10);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function parsePositiveNumber(raw, fallback) {
  const value = Number.parseFloat(String(raw || ''));
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function parseRate(raw, fallback) {
  const value = Number.parseFloat(String(raw || ''));
  return Number.isFinite(value) && value >= 0 && value <= 1 ? value : fallback;
}

function round(value, digits = 2) {
  const factor = 10 ** digits;
  return Math.round(value * factor) / factor;
}

function delay(ms) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms));
}

export { evaluateFleetReports, readFleetConfig };
