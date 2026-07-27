import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  calculateOfferedRate,
  counterDelta,
  evaluateBidThresholds,
  hasMetricFamily,
  metricDelta,
  metricLabels,
  metricValue,
  parseNonNegativeInt,
  parseNonNegativeNumber,
  parsePositiveInt,
  parsePositiveNumber,
  parseRate,
  percentile,
  requireMetricFamily,
  resolveMetricsEndpoints,
  round,
  runOpenArrivalBids,
  waitForLoadBarrier,
  validateMetricsEndpoints,
} from './load-bid-hot-path.mjs';

test('metricValue sums one Prometheus family without matching longer prefixes', () => {
  const metrics = [
    '# HELP auction_ws_events_sent_total sent events',
    'auction_ws_events_sent_total{type="BID_PLACED"} 12',
    'auction_ws_events_sent_total{type="RANKING_UPDATED"} 8',
    'auction_ws_events_sent_total_extra 999',
    'auction_projection_pending_count 3',
  ].join('\n');

  assert.equal(metricValue(metrics, 'auction_ws_events_sent_total'), 20);
  assert.equal(metricValue(metrics, 'auction_projection_pending_count'), 3);
  assert.equal(metricValue(metrics, 'missing_metric'), 0);
});

test('metric deltas preserve gauges while counter deltas never go negative', () => {
  const before = 'process_resident_memory_bytes 1200\nauction_projection_failed_total 5\n';
  const after = 'process_resident_memory_bytes 1000\nauction_projection_failed_total 3\n';

  assert.equal(metricDelta(before, after, 'process_resident_memory_bytes'), -200);
  assert.equal(counterDelta(before, after, 'auction_projection_failed_total'), 0);
});

test('metricValue filters labeled outcomes without summing failures into successes', () => {
  const metrics = [
    'auction_outbox_ack_result_total{result="ok",shard="0"} 4',
    'auction_outbox_ack_result_total{result="ok",shard="1"} 3',
    'auction_outbox_ack_result_total{result="mismatch",shard="1"} 2',
  ].join('\n');

  assert.equal(metricValue(metrics, 'auction_outbox_ack_result_total'), 9);
  assert.equal(metricValue(metrics, 'auction_outbox_ack_result_total', { result: 'ok' }), 7);
  assert.equal(metricValue(metrics, 'auction_outbox_ack_result_total', { result: 'mismatch' }), 2);
  assert.deepEqual(metricLabels('{result="ok",detail="escaped\\\\value"} 1'), {
    result: 'ok',
    detail: 'escaped\\value',
  });
});

test('required metric families accept metadata or samples and reject absent worker metrics', () => {
  assert.equal(hasMetricFamily('# HELP auction_projection_lag_records lag\n', 'auction_projection_lag_records'), true);
  assert.equal(hasMetricFamily('auction_projection_lag_records{partition="0"} 0\n', 'auction_projection_lag_records'), true);
  assert.equal(hasMetricFamily('auction_projection_lag_records_extra 1\n', 'auction_projection_lag_records'), false);
  assert.doesNotThrow(() => requireMetricFamily('# TYPE auction_outbox_pending gauge\n', 'auction_outbox_pending', 'relay'));
  assert.throws(
    () => requireMetricFamily('go_build_info 1\n', 'auction_outbox_pending', 'relay'),
    /relay metrics endpoint does not expose required family auction_outbox_pending/,
  );
});

test('split deployment requires explicit and distinct worker metrics endpoints', () => {
  const baseUrl = 'http://127.0.0.1:18080';
  const missing = resolveMetricsEndpoints({}, baseUrl);
  assert.deepEqual(missing, {
    gateway: `${baseUrl}/metrics`,
    projector: '',
    relay: '',
  });
  assert.throws(() => validateMetricsEndpoints(missing, false), /PROJECTOR_METRICS_URL is required/);

  const split = resolveMetricsEndpoints({
    PROJECTOR_METRICS_URL: 'http://127.0.0.1:18083/metrics/',
    RELAY_METRICS_URL: 'http://127.0.0.1:18082/metrics/',
  }, baseUrl);
  assert.doesNotThrow(() => validateMetricsEndpoints(split, false));
  assert.equal(split.projector, 'http://127.0.0.1:18083/metrics');

  const shared = { ...split, projector: split.gateway };
  assert.throws(() => validateMetricsEndpoints(shared, false), /must be distinct from Gateway/);
  assert.doesNotThrow(() => validateMetricsEndpoints(shared, true));
});

test('percentile uses nearest-rank semantics and handles an empty sample', () => {
  const values = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

  assert.equal(percentile(values, 50), 5);
  assert.equal(percentile(values, 95), 10);
  assert.equal(percentile([], 99), 0);
});

test('numeric environment parsing rejects invalid ranges', () => {
  assert.equal(parsePositiveInt('12', 5), 12);
  assert.equal(parsePositiveInt('0', 5), 5);
  assert.equal(parseNonNegativeInt('0', 5), 0);
  assert.equal(parseNonNegativeInt('-1', 5), 5);
  assert.equal(parseNonNegativeNumber('0', 5), 0);
  assert.equal(parseNonNegativeNumber('-1', 5), 5);
  assert.equal(parsePositiveNumber('100.5', 10), 100.5);
  assert.equal(parsePositiveNumber('0', 10), 10);
  assert.equal(parseRate('0.001', 0), 0.001);
  assert.equal(parseRate('1.1', 0), 0);
  assert.equal(round(1.236, 2), 1.24);
});

test('bid SLA thresholds fail the process boundary instead of only reporting metrics', () => {
  const thresholds = {
    bidP99LimitMs: 200,
    minBidThroughputPerSecond: 100,
    maxSystemErrorRate: 0,
    targetBidRatePerSecond: 0,
    minOfferedRateRatio: 0.99,
    maxScheduleDriftP99Ms: 100,
  };
  assert.deepEqual(evaluateBidThresholds({
    total: 5000,
    systemErrors: 0,
    errorRate: 0,
    throughputPerSecond: 200,
    p99Ms: 199,
  }, thresholds), []);

  const failures = evaluateBidThresholds({
    total: 5000,
    systemErrors: 5,
    errorRate: 0.001,
    throughputPerSecond: 99.9,
    p99Ms: 200,
  }, thresholds);
  assert.equal(failures.length, 3);
  assert.match(failures[0], /system error rate/);
  assert.match(failures[1], /must be below 200ms/);
  assert.match(failures[2], /below 100\/s/);
});

test('open arrival-rate threshold distinguishes generator drift from service latency', () => {
  const failures = evaluateBidThresholds({
    total: 100,
    systemErrors: 0,
    errorRate: 0,
    throughputPerSecond: 100,
    p99Ms: 100,
    offeredRatePerSecond: 95,
    scheduleDriftP99Ms: 150,
  }, {
    bidP99LimitMs: 200,
    minBidThroughputPerSecond: 95,
    maxSystemErrorRate: 0,
    targetBidRatePerSecond: 100,
    minOfferedRateRatio: 0.99,
    maxScheduleDriftP99Ms: 100,
  });
  assert.equal(failures.length, 2);
  assert.match(failures[0], /offered bid rate/);
  assert.match(failures[1], /schedule drift/);
});

test('open arrival scheduler preserves the requested order and reports start offsets', async () => {
  const startedAt = performance.now();
  const results = await runOpenArrivalBids(4, 200, startedAt, async (index) => ({ index, ms: 1 }));
  assert.deepEqual(results.map((item) => item.index), [0, 1, 2, 3]);
  assert.ok(results.every((item) => item.startedOffsetMs >= item.scheduledOffsetMs));
  assert.ok(calculateOfferedRate(results) > 0);
  assert.equal(calculateOfferedRate([{ startedOffsetMs: 10 }]), 0);
});

test('load barrier records readiness and waits for the shared future start', async (t) => {
  const scratch = await mkdtemp(join(tmpdir(), 'auction-bid-barrier-'));
  t.after(() => rm(scratch, { recursive: true, force: true }));
  const readyFile = join(scratch, 'scenario.ready.json');
  const startFile = join(scratch, 'start.json');
  const barrier = waitForLoadBarrier({ readyFile, startFile, timeoutMs: 1000, runId: 'barrier-test' });
  await new Promise((resolve) => setTimeout(resolve, 20));
  const readiness = JSON.parse(await readFile(readyFile, 'utf8'));
  assert.equal(readiness.runId, 'barrier-test');
  const startAtUnixMs = Date.now() + 20;
  await writeFile(startFile, JSON.stringify({ startAtUnixMs }), 'utf8');
  const result = await barrier;
  assert.deepEqual(result, { coordinated: true, startAtUnixMs });
  assert.ok(Date.now() >= startAtUnixMs);
});

test('load barrier rejects partial configuration', async () => {
  await assert.rejects(
    waitForLoadBarrier({ readyFile: '/tmp/only-ready', startFile: '', timeoutMs: 10, runId: 'test' }),
    /must be configured together/,
  );
});
