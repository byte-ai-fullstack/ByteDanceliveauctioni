import assert from 'node:assert/strict';
import test from 'node:test';

import { evaluateFleetReports, readFleetConfig } from './load-bid-fleet.mjs';

function passingReport(startAt, offeredRate = 105) {
  return {
    status: 'PASS',
    total: 6300,
    systemErrors: 0,
    p99Ms: 180,
    bidBatchStartedAtUnixMs: startAt,
    bidBatchEndedAtUnixMs: startAt + 60_100,
    loadProfile: { offeredRatePerSecond: offeredRate },
  };
}

test('fleet config defaults exceed the 200 per second blueprint floor', () => {
  const config = readFleetConfig({ RUN_ID: 'fleet-test', CONFIRM_BID_LOAD: '1' });
  assert.equal(config.scenarios, 2);
  assert.equal(config.ratePerScenario, 105);
  assert.equal(config.minAggregateOfferedRate, 200);
  assert.equal(config.bidP99LimitMs, 200);
  assert.equal(config.maxSystemErrorRate, 0);
  assert.equal(config.confirmed, true);
});

test('fleet aggregation requires overlapping steady windows and hard SLA success', () => {
  const config = readFleetConfig({ RUN_ID: 'fleet-pass', CONFIRM_BID_LOAD: '1' });
  const evaluation = evaluateFleetReports([
    passingReport(1_000),
    passingReport(1_050),
  ], config);
  assert.deepEqual(evaluation.failures, []);
  assert.equal(evaluation.totalRequests, 12_600);
  assert.equal(evaluation.aggregateOfferedRatePerSecond, 210);
  assert.equal(evaluation.aggregateP99UpperBoundMs, 180);
  assert.ok(evaluation.overlapRatio >= 0.99);
});

test('fleet aggregation rejects sequential reports, low offered rate and failed child', () => {
  const config = readFleetConfig({ RUN_ID: 'fleet-fail', CONFIRM_BID_LOAD: '1' });
  const second = passingReport(70_000, 80);
  second.status = 'FAIL';
  second.systemErrors = 2;
  second.p99Ms = 250;
  const evaluation = evaluateFleetReports([
    passingReport(1_000, 80),
    second,
  ], config);
  const failures = evaluation.failures.join('; ');
  assert.match(failures, /scenario 2 status is FAIL/);
  assert.match(failures, /overlap ratio/);
  assert.match(failures, /aggregate offered rate/);
  assert.match(failures, /aggregate system error rate/);
  assert.match(failures, /aggregate p99 upper bound/);
});

test('fleet config rejects unsafe run identifiers', () => {
  assert.throws(() => readFleetConfig({ RUN_ID: '../escape' }), /must be 1-32/);
  assert.throws(() => readFleetConfig({ RUN_ID: 'UPPERCASE' }), /must be 1-32/);
  assert.throws(() => readFleetConfig({ RUN_ID: 'x'.repeat(33) }), /must be 1-32/);
});
