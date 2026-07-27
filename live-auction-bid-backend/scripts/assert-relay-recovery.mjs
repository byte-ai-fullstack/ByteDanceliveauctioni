#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

import { metricValue, requireMetricFamily } from './load-bid-hot-path.mjs';
import {
  accountIdentity,
  apiRequest,
  bidAccepted,
  createLiveLot,
  ensureAccount,
  ensureDepositHeld,
  firstAdminRoom,
  isHealthyProjection,
  loginAccount,
  normalizeURL,
  operationRequest,
  partitionLatestSum,
  placeBid,
  positiveInt,
  projectionSnapshot,
  queryMySQLJSON,
  readDatabaseEvidence,
  sqlQuote,
  waitForProjection,
  writeJSON,
} from './assert-projector-recovery.mjs';

const OUTBOX_SHARDS = 16;
const LOT_STATUS_LIVE = 2;
const LOT_STATUS_EXTENDED = 7;
const ACK_FAILURE_RESULTS = ['empty', 'malformed', 'mismatch', 'not_owner', 'error'];

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch((error) => {
    console.error(error?.stack || error);
    process.exit(1);
  });
}

async function main() {
  const config = readConfig(process.env, process.argv);
  switch (config.phase) {
    case 'before':
      await runBefore(config);
      break;
    case 'during':
      await runDuring(config);
      break;
    case 'after':
      await runAfter(config);
      break;
    default:
      throw new Error(`unsupported Relay assertion phase: ${config.phase}`);
  }
}

function readConfig(env, argv) {
  const phase = env.FAULT_PHASE || argv[2] || '';
  const scenario = env.FAULT_SCENARIO || argv[3] || '';
  const resultDirValue = env.FAULT_RESULT_DIR || argv[4] || '';
  const composeFileValue = env.FAULT_COMPOSE_FILE || '';
  const service = env.FAULT_SERVICE || argv[5] || '';
  if (!['relay-pause', 'relay-kill'].includes(scenario) || service !== 'outbox-relay') {
    throw new Error(`assert-relay-recovery only supports relay-pause/relay-kill, got ${scenario}/${service}`);
  }
  if (!resultDirValue) throw new Error('FAULT_RESULT_DIR is required');
  if (!composeFileValue) throw new Error('FAULT_COMPOSE_FILE is required');
  const resultDir = resolve(resultDirValue);
  return {
    phase,
    scenario,
    resultDir,
    stateFile: resolve(resultDir, 'relay-assertion-state.json'),
    baseUrl: normalizeURL(env.BASE_URL || 'http://127.0.0.1:18080'),
    operationsUrl: normalizeURL(env.AUCTION_SERVICE_OPERATIONS_URL || 'http://127.0.0.1:18086'),
    relayMetricsUrl: normalizeURL(env.RELAY_METRICS_URL || 'http://127.0.0.1:18082/metrics'),
    composeFile: resolve(composeFileValue),
    mysqlService: env.FAULT_MYSQL_SERVICE || 'mysql',
    redisService: env.FAULT_REDIS_SERVICE || 'redis',
    redisPassword: env.FAULT_REDIS_PASSWORD || 'auction_redis',
    pollMs: positiveInt(env.RELAY_ASSERTION_POLL_MS, 100),
    recoveryTimeoutMs: positiveInt(env.RELAY_ASSERTION_RECOVERY_TIMEOUT_MS, 45_000),
    bidCount: positiveInt(env.RELAY_ASSERTION_BID_COUNT, 12),
  };
}

async function runBefore(config) {
  await assertServiceAdmission(config);
  const projectionBefore = await projectionSnapshot(config);
  if (!isHealthyProjection(projectionBefore)) throw new Error(`projection is not drained before Relay drill: ${JSON.stringify(projectionBefore)}`);
  const outboxBefore = await waitForOutbox(config, (snapshot) => snapshot.pending === 0 && snapshot.inflight === 0, 'baseline outbox drain');

  const identity = createHash('sha256').update(config.resultDir).digest('hex').slice(0, 12);
  const merchant = await ensureAccount(config, 'merchants', `relay_m_${identity}`, 'merchant');
  const buyers = [
    await ensureAccount(config, 'users', `relay_b0_${identity}`, 'buyer'),
    await ensureAccount(config, 'users', `relay_b1_${identity}`, 'buyer'),
  ];
  const room = await firstAdminRoom(config, merchant.token);
  const lot = await createLiveLot(config, merchant.token, room.id, `Relay ${identity}`);
  for (const [index, buyer] of buyers.entries()) {
    await ensureDepositHeld(config, buyer, lot.id, `relay-${identity}-${index}`);
  }
  const baselineIdempotencyKey = `relay-${identity}-baseline`;
  const baselineBid = await placeBid(config, buyers[0].token, lot.id, 10_100, baselineIdempotencyKey);
  if (!bidAccepted(baselineBid)) throw new Error(`Relay baseline bid was not accepted: ${JSON.stringify(baselineBid)}`);

  await waitForProjection(config, isHealthyProjection, config.recoveryTimeoutMs, 'Relay baseline projection');
  const baselineDatabase = await waitForValue(config, async () => {
    const evidence = await readDatabaseEvidence(config, {
      lotId: lot.id,
      acceptedIdempotencyKey: baselineIdempotencyKey,
      rejectedIdempotencyKey: '',
      baselineDetectedAtMs: 0,
    });
    return evidence.acceptedBidCount === 1 ? evidence : null;
  }, 'Relay baseline bid visibility');
  const drainedOutbox = await waitForOutbox(config, (snapshot) => snapshot.pending === 0 && snapshot.inflight === 0, 'Relay baseline ACK');
  const drainedProjection = await projectionSnapshot(config);
  if (!isHealthyProjection(drainedProjection)) throw new Error('projection became unhealthy while preparing Relay drill');
  const relayMetricsBefore = await readRelayMetrics(config);

  const state = {
    schemaVersion: 1,
    scenario: config.scenario,
    identity,
    baselineDetectedAtMs: baselineDatabase.databaseNowMs,
    baselineDomainPending: baselineDatabase.domainPending,
    merchant: accountIdentity(merchant),
    buyers: buyers.map(accountIdentity),
    roomId: room.id,
    lotId: lot.id,
    baselineAmount: 10_100,
    baselineIdempotencyKey,
    baselineOutbox: drainedOutbox,
    baselineProjectionLatest: partitionLatestSum(drainedProjection),
    baselineAck: ackCounters(relayMetricsBefore),
    baselineEpochSum: outboxBefore.epochSum,
  };
  await writeJSON(config.stateFile, state);
  await writeJSON(resolve(config.resultDir, 'relay-before.json'), {
    projectionBefore,
    drainedProjection,
    outboxBefore,
    drainedOutbox,
    baselineDatabase,
    ack: state.baselineAck,
  });
  console.log(JSON.stringify({ status: 'PASS', phase: 'before', lotId: lot.id, outbox: drainedOutbox }));
}

async function runDuring(config) {
  const state = await readState(config);
  const buyers = await Promise.all(state.buyers.map((buyer) => loginAccount(config, buyer.username)));
  await assertServiceAdmission(config);
  const projectionBefore = await projectionSnapshot(config);
  const idempotencyPrefix = `relay-${state.identity}-bid-`;
  const accepted = [];
  for (let index = 0; index < config.bidCount; index += 1) {
    const amount = Number(state.baselineAmount) + (index + 1) * 100;
    const reply = await placeBid(
      config,
      buyers[(index + 1) % buyers.length].token,
      state.lotId,
      amount,
      `${idempotencyPrefix}${index}`,
    );
    if (!bidAccepted(reply)) throw new Error(`Relay fault-window bid ${index} was not accepted: ${JSON.stringify(reply)}`);
    accepted.push({ index, amount, bidId: reply.bid?.id || '' });
  }
  const outboxDuring = await waitForOutbox(
    config,
    (snapshot) => snapshot.pending + snapshot.inflight >= config.bidCount,
    'Relay fault-window outbox accumulation',
  );
  const projectionAfter = await waitForProjection(
    config,
    (snapshot) => snapshot.checkedAtMs > projectionBefore.checkedAtMs,
    config.recoveryTimeoutMs,
    'Relay fault-window projection sample',
  );
  const latestBefore = partitionLatestSum(projectionBefore);
  const latestAfter = partitionLatestSum(projectionAfter);
  if (latestAfter !== latestBefore) {
    throw new Error(`Relay outage unexpectedly advanced Kafka projection watermarks: before=${latestBefore} after=${latestAfter}`);
  }
  if (!isHealthyProjection(projectionAfter)) {
    throw new Error(`Projector must remain drained while facts are retained in Redis: ${JSON.stringify(projectionAfter)}`);
  }
  Object.assign(state, {
    idempotencyPrefix,
    bidCount: config.bidCount,
    finalAmount: accepted.at(-1).amount,
    faultWindowProjectionLatest: latestAfter,
    outboxDuring,
  });
  await writeJSON(config.stateFile, state);
  await writeJSON(resolve(config.resultDir, 'relay-during.json'), {
    accepted,
    outboxDuring,
    projectionBefore,
    projectionAfter,
  });
  console.log(JSON.stringify({ status: 'PASS', phase: 'during', accepted: accepted.length, outbox: outboxDuring }));
}

async function runAfter(config) {
  const state = await readState(config);
  if (!state.idempotencyPrefix || !Number.isInteger(state.bidCount) || state.bidCount <= 0) {
    throw new Error('Relay during phase did not persist its accepted bid evidence');
  }
  const outboxAfter = await waitForOutbox(config, (snapshot) => (
    snapshot.pending === 0 && snapshot.inflight === 0 && snapshot.ownerCount === OUTBOX_SHARDS
  ), 'Relay takeover and outbox drain');
  if (config.scenario === 'relay-kill' && outboxAfter.epochSum <= Number(state.baselineEpochSum)) {
    throw new Error(`Relay kill did not advance fenced ownership epochs: before=${state.baselineEpochSum} after=${outboxAfter.epochSum}`);
  }
  const projectionAfter = await waitForProjection(config, (snapshot) => (
    isHealthyProjection(snapshot) && partitionLatestSum(snapshot) >= Number(state.faultWindowProjectionLatest) + state.bidCount
  ), config.recoveryTimeoutMs, 'Relay Kafka and MySQL recovery');
  const database = await waitForValue(config, async () => {
    const evidence = readRelayDatabaseEvidence(config, state);
    return evaluateRelayDatabase(evidence, state).length === 0 ? evidence : null;
  }, 'Relay MySQL continuity');
  const relayMetricsAfter = await readRelayMetrics(config);
  const ackAfter = ackCounters(relayMetricsAfter);
  const ackFailures = config.scenario === 'relay-pause'
    ? ackFailureTotal(counterDifference(state.baselineAck, ackAfter))
    : ackFailureTotal(ackAfter);
  const ackOK = config.scenario === 'relay-pause'
    ? Math.max(0, ackAfter.ok - Number(state.baselineAck.ok || 0))
    : ackAfter.ok;
  if (ackFailures !== 0) throw new Error(`Relay ACK invariant failures increased by ${ackFailures}`);
  if (ackOK < state.bidCount) throw new Error(`Relay successful ACK count=${ackOK} is below recovered facts=${state.bidCount}`);
  const evidence = { outboxAfter, projectionAfter, database, ackAfter, ackOK, ackFailures };
  await writeJSON(resolve(config.resultDir, 'relay-after.json'), evidence);
  console.log(JSON.stringify({
    status: 'PASS', phase: 'after', bidCount: state.bidCount,
    projectionVersion: database.projectionVersion, ackOK, ackFailures,
  }));
}

async function assertServiceAdmission(config) {
  const [ready, admission] = await Promise.all([
    operationRequest(config, '/readyz'),
    operationRequest(config, '/admissionz'),
  ]);
  if (ready.status !== 200 || admission.status !== 200) {
    throw new Error(`auction-service is not ready/open: ready=${ready.status} admission=${admission.status}`);
  }
}

async function readRelayMetrics(config) {
  const response = await fetch(config.relayMetricsUrl, { signal: AbortSignal.timeout(5_000) });
  if (!response.ok) throw new Error(`GET Relay metrics HTTP ${response.status}`);
  const metrics = await response.text();
  requireMetricFamily(metrics, 'auction_outbox_ack_result_total', 'outbox-relay');
  requireMetricFamily(metrics, 'auction_outbox_owner', 'outbox-relay');
  return metrics;
}

function ackCounters(metrics) {
  const result = { ok: metricValue(metrics, 'auction_outbox_ack_result_total', { result: 'ok' }) };
  for (const label of ACK_FAILURE_RESULTS) {
    result[label] = metricValue(metrics, 'auction_outbox_ack_result_total', { result: label });
  }
  return result;
}

function counterDifference(before, after) {
  return Object.fromEntries(Object.keys(after).map((key) => [key, Math.max(0, Number(after[key]) - Number(before[key] || 0))]));
}

function ackFailureTotal(counters) {
  return ACK_FAILURE_RESULTS.reduce((total, label) => total + Number(counters[label] || 0), 0);
}

function queryRedisOutbox(config) {
  const lua = `local pending=0 local inflight=0 local owners=0 local epochs=0
for shard=0,${OUTBOX_SHARDS - 1} do
  pending=pending+redis.call('LLEN','auction:runtime:outbox:pending:'..shard)
  inflight=inflight+redis.call('LLEN','auction:runtime:outbox:inflight:'..shard)
  if redis.call('GET','auction:runtime:outbox:owner:'..shard) then owners=owners+1 end
  epochs=epochs+tonumber(redis.call('GET','auction:runtime:outbox:epoch:'..shard) or '0')
end
return cjson.encode({pending=pending,inflight=inflight,ownerCount=owners,epochSum=epochs})`;
  const result = spawnSync('docker', [
    'compose', '-f', config.composeFile, 'exec', '-T',
    config.redisService, 'redis-cli', '--raw',
  ], {
    input: `AUTH ${redisCLIQuote(config.redisPassword)}\nEVAL ${redisCLIQuote(lua)} 0\n`,
    encoding: 'utf8',
    maxBuffer: 1024 * 1024,
  });
  if (result.error) throw new Error(`query Redis outbox: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`Redis outbox query exited ${result.status}: ${String(result.stderr).trim()}`);
  const output = String(result.stdout).trim().split('\n').at(-1) || '';
  try {
    const parsed = JSON.parse(output);
    return Object.fromEntries(Object.entries(parsed).map(([key, value]) => [key, Number(value)]));
  } catch {
    throw new Error(`Redis outbox query returned invalid JSON: ${output.slice(0, 240)}`);
  }
}

function redisCLIQuote(value) {
  return `"${String(value)
    .replaceAll('\\', '\\\\')
    .replaceAll('"', '\\"')
    .replaceAll('\r', '\\r')
    .replaceAll('\n', '\\n')}"`;
}

async function waitForOutbox(config, predicate, label) {
  return waitForValue(config, async () => {
    const snapshot = queryRedisOutbox(config);
    return predicate(snapshot) ? snapshot : null;
  }, label);
}

async function waitForValue(config, read, label) {
  const deadline = Date.now() + config.recoveryTimeoutMs;
  let latest;
  do {
    latest = await read();
    if (latest) return latest;
    await new Promise((resolveDelay) => setTimeout(resolveDelay, config.pollMs));
  } while (Date.now() < deadline);
  throw new Error(`${label} timed out after ${config.recoveryTimeoutMs}ms; latest=${JSON.stringify(latest)}`);
}

function readRelayDatabaseEvidence(config, state) {
  const lot = sqlQuote(state.lotId);
  const pattern = sqlQuote(`${state.idempotencyPrefix}%`);
  const detectedAt = Number(state.baselineDetectedAtMs);
  const sql = `SELECT JSON_OBJECT(
    'acceptedBidCount', (SELECT COUNT(*) FROM auction_bids WHERE lot_id=${lot} AND idempotency_key LIKE ${pattern}),
    'distinctIdempotencyCount', (SELECT COUNT(DISTINCT idempotency_key) FROM auction_bids WHERE lot_id=${lot} AND idempotency_key LIKE ${pattern}),
    'minimumBidAmount', COALESCE((SELECT MIN(amount) FROM auction_bids WHERE lot_id=${lot} AND idempotency_key LIKE ${pattern}), -1),
    'maximumBidAmount', COALESCE((SELECT MAX(amount) FROM auction_bids WHERE lot_id=${lot} AND idempotency_key LIKE ${pattern}), -1),
    'lotStatus', COALESCE((SELECT status FROM auction_lots WHERE id=${lot}), -1),
    'currentPrice', COALESCE((SELECT current_price_amount FROM auction_lots WHERE id=${lot}), -1),
    'lotVersion', COALESCE((SELECT version FROM auction_lots WHERE id=${lot}), -1),
    'projectionVersion', COALESCE((SELECT last_lot_version FROM auction_lot_projection_state WHERE lot_id=${lot}), -1),
    'inboxMinimumVersion', COALESCE((SELECT MIN(lot_version) FROM auction_projection_inbox WHERE lot_id=${lot}), -1),
    'inboxMaximumVersion', COALESCE((SELECT MAX(lot_version) FROM auction_projection_inbox WHERE lot_id=${lot}), -1),
    'inboxDistinctVersions', (SELECT COUNT(DISTINCT lot_version) FROM auction_projection_inbox WHERE lot_id=${lot}),
    'domainPending', (SELECT COUNT(*) FROM auction_domain_outbox WHERE published_at_ms=0),
    'newHighFindings', (SELECT COUNT(*) FROM auction_reconcile_findings WHERE severity IN ('P0','P1') AND resolved_at_ms=0 AND detected_at_ms >= ${detectedAt})
  );`;
  return queryMySQLJSON(config, sql);
}

function evaluateRelayDatabase(database, state) {
  const failures = [];
  const expectedCount = Number(state.bidCount);
  const expectedMinimum = Number(state.baselineAmount) + 100;
  const expectedMaximum = Number(state.finalAmount);
  if (database.acceptedBidCount !== expectedCount) failures.push(`accepted bids=${database.acceptedBidCount} want=${expectedCount}`);
  if (database.distinctIdempotencyCount !== expectedCount) failures.push(`distinct idempotency=${database.distinctIdempotencyCount} want=${expectedCount}`);
  if (database.minimumBidAmount !== expectedMinimum || database.maximumBidAmount !== expectedMaximum) {
    failures.push(`bid range=${database.minimumBidAmount}..${database.maximumBidAmount} want=${expectedMinimum}..${expectedMaximum}`);
  }
  if (![LOT_STATUS_LIVE, LOT_STATUS_EXTENDED].includes(database.lotStatus)) failures.push(`lot status=${database.lotStatus} is not active`);
  if (database.currentPrice !== expectedMaximum) failures.push(`current price=${database.currentPrice} want=${expectedMaximum}`);
  if (database.lotVersion !== database.projectionVersion || database.lotVersion !== database.inboxMaximumVersion) {
    failures.push(`version chain lot=${database.lotVersion} projection=${database.projectionVersion} inbox=${database.inboxMaximumVersion}`);
  }
  const expectedInboxVersions = database.inboxMaximumVersion - database.inboxMinimumVersion + 1;
  if (database.inboxDistinctVersions !== expectedInboxVersions) {
    failures.push(`inbox version gap distinct=${database.inboxDistinctVersions} expected=${expectedInboxVersions}`);
  }
  if (database.newHighFindings !== 0) failures.push(`new unresolved P0/P1 findings=${database.newHighFindings}`);
  if (database.domainPending > Number(state.baselineDomainPending)) {
    failures.push(`domain pending=${database.domainPending} baseline=${state.baselineDomainPending}`);
  }
  return failures;
}

async function readState(config) {
  const state = JSON.parse(await readFile(config.stateFile, 'utf8'));
  if (state.schemaVersion !== 1 || state.scenario !== config.scenario || !state.lotId) {
    throw new Error('Relay assertion state does not match this fault run');
  }
  return state;
}

export {
  ackCounters,
  ackFailureTotal,
  evaluateRelayDatabase,
  queryRedisOutbox,
  redisCLIQuote,
};
