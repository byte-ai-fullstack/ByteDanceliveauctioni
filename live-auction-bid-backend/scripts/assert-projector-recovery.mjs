#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const RESULT_CODE_OK = 0;
const RESULT_CODE_OVERLOADED = 503001;
const LOT_STATUS_CANCELLED = 4;

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch((error) => {
    console.error(error?.stack || error);
    process.exit(1);
  });
}

async function main() {
  const config = readConfig(process.env, process.argv);
  await mkdir(config.resultDir, { recursive: true });
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
      throw new Error(`unsupported fault assertion phase: ${config.phase}`);
  }
}

function readConfig(env, argv) {
  const phase = env.FAULT_PHASE || argv[2] || '';
  const scenario = env.FAULT_SCENARIO || argv[3] || '';
  const resultDir = resolve(env.FAULT_RESULT_DIR || argv[4] || '');
  const composeFileValue = env.FAULT_COMPOSE_FILE || '';
  const service = env.FAULT_SERVICE || argv[5] || '';
  if (!['projector-pause', 'projector-kill'].includes(scenario) || service !== 'projector') {
    throw new Error(`assert-projector-recovery only supports projector-pause/projector-kill, got ${scenario}/${service}`);
  }
  if (!resultDir || resultDir === resolve('')) throw new Error('FAULT_RESULT_DIR is required');
  if (!composeFileValue) throw new Error('FAULT_COMPOSE_FILE is required');
  return {
    phase,
    scenario,
    resultDir,
    stateFile: resolve(resultDir, 'projector-assertion-state.json'),
    baseUrl: normalizeURL(env.BASE_URL || 'http://127.0.0.1:18080'),
    operationsUrl: normalizeURL(env.AUCTION_SERVICE_OPERATIONS_URL || 'http://127.0.0.1:18086'),
    composeFile: resolve(composeFileValue),
    mysqlService: env.FAULT_MYSQL_SERVICE || 'mysql',
    pollMs: positiveInt(env.PROJECTOR_ASSERTION_POLL_MS, 500),
    closeTimeoutMs: positiveInt(env.PROJECTOR_ASSERTION_CLOSE_TIMEOUT_MS, 45_000),
    recoveryTimeoutMs: positiveInt(env.PROJECTOR_ASSERTION_RECOVERY_TIMEOUT_MS, 45_000),
    observeRejectedMs: positiveInt(env.PROJECTOR_ASSERTION_REJECT_OBSERVE_MS, 10_000),
    requireRecoveringEvidence: env.PROJECTOR_ASSERTION_REQUIRE_RECOVERING === '1' ||
      (env.PROJECTOR_ASSERTION_REQUIRE_RECOVERING !== '0' && scenario === 'projector-pause'),
  };
}

async function runBefore(config) {
  await assertOperationsHealthy(config, true);
  const initialProjection = await projectionSnapshot(config);
  assertHealthyProjection(initialProjection, 'before');

  const identity = createHash('sha256').update(config.resultDir).digest('hex').slice(0, 12);
  const merchantAccount = await ensureAccount(config, 'merchants', `fault_m_${identity}`, 'merchant');
  const buyers = [
    await ensureAccount(config, 'users', `fault_b0_${identity}`, 'buyer'),
    await ensureAccount(config, 'users', `fault_b1_${identity}`, 'buyer'),
  ];
  const room = await firstAdminRoom(config, merchantAccount.token);
  const lot = await createLiveLot(config, merchantAccount.token, room.id, identity);
  for (const [index, buyer] of buyers.entries()) {
    await ensureDepositHeld(config, buyer, lot.id, `projector-${identity}-${index}`);
  }
  const baselineIdempotencyKey = `fault-${identity}-baseline`;
  const baselineBid = await placeBid(config, buyers[0].token, lot.id, 10_100, baselineIdempotencyKey);
  if (!bidAccepted(baselineBid)) throw new Error(`baseline bid was not accepted: ${JSON.stringify(baselineBid)}`);

  const projection = await waitForProjection(config, (snapshot) => isHealthyProjection(snapshot), config.recoveryTimeoutMs, 'before-drain');
  const baselineDatabase = await waitForDatabase(config, async () => {
    const snapshot = await readDatabaseEvidence(config, {
      lotId: lot.id,
      acceptedIdempotencyKey: baselineIdempotencyKey,
      rejectedIdempotencyKey: '',
      baselineDetectedAtMs: 0,
    });
    return snapshot.acceptedBidCount === 1 ? snapshot : null;
  }, config.recoveryTimeoutMs, 'baseline bid projection');

  const state = {
    schemaVersion: 1,
    scenario: config.scenario,
    identity,
    startedAt: new Date().toISOString(),
    baselineDetectedAtMs: baselineDatabase.databaseNowMs,
    baselineDomainPending: baselineDatabase.domainPending,
    baselineUnresolvedHighFindings: baselineDatabase.unresolvedHighFindings,
    merchant: accountIdentity(merchantAccount),
    buyers: buyers.map(accountIdentity),
    roomId: room.id,
    lotId: lot.id,
    baselineIdempotencyKey,
    baselineBidId: bidID(baselineBid),
    baselineAmount: 10_100,
    initialProjection,
    drainedProjection: projection,
  };
  await writeJSON(config.stateFile, state);
  await writeJSON(resolve(config.resultDir, 'projector-before.json'), { projection, database: baselineDatabase });
  console.log(JSON.stringify({ status: 'PASS', phase: 'before', lotId: lot.id, projectionLag: projection.totalLagRecords }));
}

async function runDuring(config) {
  const state = await readState(config);
  const merchant = await loginAccount(config, state.merchant.username);
  const buyers = await Promise.all(state.buyers.map((buyer) => loginAccount(config, buyer.username)));
  await assertReadyRemainsAvailable(config);
  const acceptedIdempotencyKey = `fault-${state.identity}-during-accepted`;
  const acceptedAmount = Number(state.baselineAmount) + 100;
  const acceptedBid = await placeBid(config, buyers[1].token, state.lotId, acceptedAmount, acceptedIdempotencyKey);
  if (!bidAccepted(acceptedBid)) throw new Error(`fault-window bid was not accepted before gate closure: ${JSON.stringify(acceptedBid)}`);
  Object.assign(state, {
    acceptedIdempotencyKey,
    acceptedBidId: bidID(acceptedBid),
    acceptedAmount,
  });
  await writeJSON(config.stateFile, state);

  const closedTimeline = [];
  const closedProjection = await waitForProjection(config, async (snapshot) => {
    const admission = await operationRequest(config, '/admissionz');
    closedTimeline.push(timelineItem(snapshot, admission.status));
    return !snapshot.ready && admission.status === 503;
  }, config.closeTimeoutMs, 'gate-close');
  if (!['lag_limit', 'oldest_age_limit'].includes(closedProjection.reason) || closedProjection.totalLagRecords <= 0) {
    throw new Error(`projection gate closed for unexpected reason ${closedProjection.reason}`);
  }
  await assertReadyRemainsAvailable(config);

  const latestBeforeRejected = partitionLatestSum(closedProjection);
  const rejectedIdempotencyKey = `fault-${state.identity}-during-rejected`;
  const rejectedBid = await placeBid(config, buyers[0].token, state.lotId, acceptedAmount + 100, rejectedIdempotencyKey);
  if (!overloadedResult(rejectedBid)) {
    throw new Error(`closed projection gate did not return OVERLOADED: ${JSON.stringify(rejectedBid)}`);
  }
  const afterRejectedProjection = await waitForProjection(
    config,
    (snapshot) => snapshot.checkedAtMs > closedProjection.checkedAtMs,
    config.observeRejectedMs,
    'post-OVERLOADED projection sample',
  );
  const latestAfterRejected = partitionLatestSum(afterRejectedProjection);
  if (latestAfterRejected !== latestBeforeRejected) {
    throw new Error(`OVERLOADED bid changed Kafka latest offsets: before=${latestBeforeRejected} after=${latestAfterRejected}`);
  }

  const cancel = await apiRequest(config, `/api/lots/${encodeURIComponent(state.lotId)}/cancel`, {
    method: 'POST',
    token: merchant.token,
    idempotencyKey: `fault-${state.identity}-cancel`,
    body: { operator_id: merchant.userId, reason: 'projector fault assertion cancellation' },
  });
  assertResultOK(cancel, 'cancel lot while projection gate is closed');
  if (!cancelledLotStatus(cancel.lot?.status ?? cancel.lot?.lot_status)) {
    throw new Error(`cancel response did not expose CANCELLED lot: ${JSON.stringify(cancel)}`);
  }
  const afterCancelProjection = await waitForProjection(
    config,
    (snapshot) => partitionLatestSum(snapshot) > latestAfterRejected,
    10_000,
    'cancel-runtime-fact',
  );
  Object.assign(state, {
    rejectedIdempotencyKey,
    rejectedAmount: acceptedAmount + 100,
    cancelEventId: cancel.event?.id || '',
    closedReason: closedProjection.reason,
    latestBeforeRejected,
    latestAfterRejected,
    latestAfterCancel: partitionLatestSum(afterCancelProjection),
  });
  await writeJSON(config.stateFile, state);
  await writeJSON(resolve(config.resultDir, 'projector-during.json'), {
    closedTimeline,
    closedProjection,
    afterRejectedProjection,
    afterCancelProjection,
    acceptedBid,
    rejectedBid,
    cancel,
  });
  console.log(JSON.stringify({
    status: 'PASS', phase: 'during', closedReason: closedProjection.reason,
    acceptedBidId: state.acceptedBidId, cancelled: true,
  }));
}

async function runAfter(config) {
  const state = await readState(config);
  if (!state.acceptedIdempotencyKey || !state.rejectedIdempotencyKey) {
    throw new Error('during phase did not persist accepted/rejected bid evidence');
  }
  const recoveryTimeline = [];
  let seenRecovering = false;
  const healthyProjection = await waitForProjection(config, async (snapshot) => {
    const admission = await operationRequest(config, '/admissionz');
    recoveryTimeline.push(timelineItem(snapshot, admission.status));
    if (snapshot.reason === 'recovering') {
      seenRecovering = true;
      if (admission.status !== 503) throw new Error('admission opened before recovering polls completed');
    }
    return isHealthyProjection(snapshot) && admission.status === 200;
  }, config.recoveryTimeoutMs, 'projection-recovery');
  if (config.requireRecoveringEvidence && !seenRecovering) {
    throw new Error('recovery evidence did not observe the projection gate recovering state');
  }
  if (healthyProjection.consecutiveHealthyPolls < 3) {
    throw new Error(`projection gate reopened after only ${healthyProjection.consecutiveHealthyPolls} healthy polls`);
  }
  await assertOperationsHealthy(config, true);

  const database = await waitForDatabase(config, async () => {
    const snapshot = await readDatabaseEvidence(config, {
      lotId: state.lotId,
      acceptedIdempotencyKey: state.acceptedIdempotencyKey,
      rejectedIdempotencyKey: state.rejectedIdempotencyKey,
      baselineDetectedAtMs: state.baselineDetectedAtMs,
    });
    return evaluateAfterDatabase(snapshot, state).length === 0 ? snapshot : null;
  }, config.recoveryTimeoutMs, 'MySQL projector recovery');
  const failures = evaluateAfterDatabase(database, state);
  if (failures.length) throw new Error(`post-recovery MySQL invariants failed: ${failures.join('; ')}`);

  const evidence = { healthyProjection, seenRecovering, recoveryTimeline, database };
  await writeJSON(resolve(config.resultDir, 'projector-after.json'), evidence);
  console.log(JSON.stringify({
    status: 'PASS', phase: 'after', lotId: state.lotId, projectionLag: healthyProjection.totalLagRecords,
    acceptedBidCount: database.acceptedBidCount, rejectedBidCount: database.rejectedBidCount,
    seenRecovering,
  }));
}

async function assertOperationsHealthy(config, admissionOpen) {
  await assertReadyRemainsAvailable(config);
  const admission = await operationRequest(config, '/admissionz');
  const expected = admissionOpen ? 200 : 503;
  if (admission.status !== expected) throw new Error(`/admissionz status=${admission.status} want=${expected}`);
}

async function assertReadyRemainsAvailable(config) {
  const ready = await operationRequest(config, '/readyz');
  if (ready.status !== 200) throw new Error(`/readyz status=${ready.status}; cancellation/close path is not routable`);
}

async function projectionSnapshot(config) {
  const response = await operationRequest(config, '/metrics/runtime-projection');
  if (response.status !== 200) throw new Error(`runtime projection metrics HTTP ${response.status}`);
  return normalizeProjectionSnapshot(response.data);
}

function normalizeProjectionSnapshot(payload) {
  const snapshot = payload?.projection_gate || payload?.projectionGate || payload;
  if (!snapshot || typeof snapshot !== 'object') throw new Error('runtime projection payload is missing projection_gate');
  const partitions = Array.isArray(snapshot.partitions) ? snapshot.partitions.map((partition) => ({
    partition: Number(partition.partition),
    earliest: Number(partition.earliest),
    databaseNextOffset: Number(partition.database_next_offset ?? partition.databaseNextOffset),
    latest: Number(partition.latest),
    lagRecords: Number(partition.lag_records ?? partition.lagRecords),
  })) : [];
  const normalized = {
    ready: Boolean(snapshot.ready),
    reason: String(snapshot.reason || ''),
    totalLagRecords: Number(snapshot.total_lag_records ?? snapshot.totalLagRecords),
    maxPartitionLagRecords: Number(snapshot.max_partition_lag_records ?? snapshot.maxPartitionLagRecords),
    oldestAgeMs: Number(snapshot.oldest_age_ms ?? snapshot.oldestAgeMs),
    consecutiveHealthyPolls: Number(snapshot.consecutive_healthy_polls ?? snapshot.consecutiveHealthyPolls),
    checkedAtMs: Number(snapshot.checked_at_ms ?? snapshot.checkedAtMs),
    partitions,
  };
  for (const key of ['totalLagRecords', 'maxPartitionLagRecords', 'oldestAgeMs', 'consecutiveHealthyPolls', 'checkedAtMs']) {
    if (!Number.isFinite(normalized[key]) || normalized[key] < 0) throw new Error(`invalid projection snapshot ${key}`);
  }
  if (!normalized.reason || !partitions.length) throw new Error('projection snapshot is structurally incomplete');
  if (partitions.some((partition) => !Number.isInteger(partition.partition) || !Number.isFinite(partition.latest))) {
    throw new Error('projection partition snapshot is invalid');
  }
  return normalized;
}

function isHealthyProjection(snapshot) {
  return snapshot.ready && snapshot.reason === 'healthy' && snapshot.totalLagRecords === 0;
}

function assertHealthyProjection(snapshot, phase) {
  if (!isHealthyProjection(snapshot)) {
    throw new Error(`${phase} projection gate is not healthy: ready=${snapshot.ready} reason=${snapshot.reason} lag=${snapshot.totalLagRecords}`);
  }
}

function partitionLatestSum(snapshot) {
  return snapshot.partitions.reduce((total, partition) => total + partition.latest, 0);
}

async function waitForProjection(config, predicate, timeoutMs, label) {
  const deadline = Date.now() + timeoutMs;
  let latest;
  do {
    latest = await projectionSnapshot(config);
    if (await predicate(latest)) return latest;
    await delay(config.pollMs);
  } while (Date.now() < deadline);
  throw new Error(`${label} timed out after ${timeoutMs}ms; latest=${JSON.stringify(latest)}`);
}

async function waitForDatabase(config, read, timeoutMs, label) {
  const deadline = Date.now() + timeoutMs;
  let latest;
  do {
    latest = await read();
    if (latest) return latest;
    await delay(config.pollMs);
  } while (Date.now() < deadline);
  throw new Error(`${label} timed out after ${timeoutMs}ms; latest=${JSON.stringify(latest)}`);
}

async function ensureAccount(config, endpoint, username, role) {
  const password = 'FaultAssert123!';
  const register = await apiRequest(config, `/api/${endpoint}/register`, {
    method: 'POST', body: { username, password, nickname: username },
  });
  const reply = tokenFrom(register) ? register : await loginReply(config, username, password);
  const token = tokenFrom(reply);
  const userId = reply.user?.id || reply.user?.user_id || '';
  if (!token || !userId) throw new Error(`cannot prepare ${role} account ${username}: ${JSON.stringify(reply)}`);
  return { username, token, userId };
}

async function loginAccount(config, username) {
  const reply = await loginReply(config, username, 'FaultAssert123!');
  const token = tokenFrom(reply);
  const userId = reply.user?.id || reply.user?.user_id || '';
  if (!token || !userId) throw new Error(`cannot login fault assertion account ${username}`);
  return { username, token, userId };
}

function loginReply(config, username, password) {
  return apiRequest(config, '/api/users/login', {
    method: 'POST', body: { username, password },
  });
}

function accountIdentity(account) {
  return { username: account.username, userId: account.userId };
}

async function createDeliveryAddress(config, buyer) {
  const reply = await apiRequest(config, '/api/shop/addresses', {
    method: 'POST',
    token: buyer.token,
    body: {
      address: {
        receiverName: `Fault ${buyer.username}`,
        phone: '13800138000',
        province: '上海市',
        city: '上海市',
        district: '浦东新区',
        street: '世纪大道',
        detail: `Fault assertion address for ${buyer.username}`,
        tag: 'fault',
        isDefault: true,
      },
    },
  });
  assertResultOK(reply, 'create assertion delivery address');
  const addressId = reply.address?.id || reply.address?.address_id || '';
  if (!addressId) throw new Error(`delivery address response is missing id: ${JSON.stringify(reply)}`);
  buyer.addressId = addressId;
  return addressId;
}

async function ensureDepositHeld(config, buyer, lotId, scope) {
  const addressId = buyer.addressId || await createDeliveryAddress(config, buyer);
  const idempotencyKey = `${scope}-deposit`;
  const reply = await apiRequest(config, `/api/lots/${encodeURIComponent(lotId)}/deposit-holds/mock-pay`, {
    method: 'POST',
    token: buyer.token,
    idempotencyKey,
    body: { addressId, idempotencyKey },
  });
  assertResultOK(reply, 'pay assertion deposit hold');
  if (!reply.paid) throw new Error(`deposit hold was not paid for buyer ${buyer.username}: ${JSON.stringify(reply)}`);
  return reply;
}

async function firstAdminRoom(config, token) {
  const reply = await apiRequest(config, '/api/admin/rooms', { token });
  assertResultOK(reply, 'list admin rooms');
  const room = reply.rooms?.[0];
  const id = room?.id || room?.room_id || '';
  if (!id) throw new Error('merchant has no admin room');
  return { ...room, id };
}

async function createLiveLot(config, token, roomId, identity) {
  const created = await apiRequest(config, '/api/lots', {
    method: 'POST', token,
    body: {
      room_id: roomId,
      title: `Projector fault ${identity}`,
      description: 'projector pause/kill deterministic assertion lot',
      image_url: 'https://example.com/projector-fault.jpg',
      rule: {
        start_price: money(10_000), min_increment: money(100), cap_price: money(1_000_000),
        duration_seconds: 300, anti_snipe_window_seconds: 15,
        anti_snipe_extend_seconds: 15, max_extend_count: 3,
      },
    },
  });
  assertResultOK(created, 'create assertion lot');
  const lotId = created.lot?.id || created.lot?.lot_id || '';
  if (!lotId) throw new Error(`create lot response is missing lot id: ${JSON.stringify(created)}`);
  const queued = await apiRequest(config, `/api/lots/${encodeURIComponent(lotId)}/queue`, {
    method: 'POST', token, body: {},
  });
  assertResultOK(queued, 'queue assertion lot');
  const started = await apiRequest(config, `/api/lots/${encodeURIComponent(lotId)}/start`, {
    method: 'POST', token, body: {},
  });
  assertResultOK(started, 'start assertion lot');
  return { ...(started.lot || created.lot), id: lotId };
}

async function placeBid(config, token, lotId, amount, idempotencyKey) {
  return apiRequest(config, `/api/lots/${encodeURIComponent(lotId)}/bid`, {
    method: 'POST', token, idempotencyKey,
    body: { amount: money(amount), idempotency_key: idempotencyKey },
  });
}

function bidAccepted(reply) {
  return Number(reply?.result?.code || 0) === RESULT_CODE_OK && Boolean(reply?.accepted || reply?.bid?.id);
}

function bidID(reply) {
  return reply?.bid?.id || reply?.bid?.bid_id || '';
}

function overloadedResult(reply) {
  const code = Number(reply?.result?.code);
  const message = String(reply?.result?.message || '');
  return code === RESULT_CODE_OVERLOADED || message === 'OVERLOADED';
}

function cancelledLotStatus(status) {
  return Number(status) === LOT_STATUS_CANCELLED || status === 'LOT_STATUS_CANCELLED';
}

function assertResultOK(reply, operation) {
  const code = Number(reply?.result?.code || 0);
  if (code !== RESULT_CODE_OK) throw new Error(`${operation} failed: ${JSON.stringify(reply?.result || reply)}`);
}

async function apiRequest(config, path, options = {}) {
  const headers = { Accept: 'application/json' };
  if (options.body !== undefined) headers['Content-Type'] = 'application/json';
  if (options.token) headers.Authorization = `Bearer ${options.token}`;
  if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey;
  return fetchJSON(`${config.baseUrl}${path}`, {
    method: options.method || 'GET', headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
}

async function operationRequest(config, path) {
  return fetchJSON(`${config.operationsUrl}${path}`, {}, true);
}

async function fetchJSON(url, options, includeStatus = false) {
  let response;
  try {
    response = await fetch(url, { ...options, signal: AbortSignal.timeout(5_000) });
  } catch (error) {
    throw new Error(`${options.method || 'GET'} ${url} failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  const text = await response.text();
  let data = {};
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      throw new Error(`${options.method || 'GET'} ${url} returned non-JSON HTTP ${response.status}: ${text.slice(0, 160)}`);
    }
  }
  if (includeStatus) return { status: response.status, data };
  if (!response.ok) throw new Error(`${options.method || 'GET'} ${url} HTTP ${response.status}: ${text.slice(0, 240)}`);
  return data;
}

async function readDatabaseEvidence(config, keys) {
  const accepted = sqlQuote(keys.acceptedIdempotencyKey);
  const rejected = sqlQuote(keys.rejectedIdempotencyKey || '__none__');
  const lot = sqlQuote(keys.lotId);
  const detectedAt = Number(keys.baselineDetectedAtMs || 0);
  const sql = `SELECT JSON_OBJECT(
    'databaseNowMs', CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000 AS UNSIGNED),
    'acceptedBidCount', (SELECT COUNT(*) FROM auction_bids WHERE lot_id=${lot} AND idempotency_key=${accepted}),
    'rejectedBidCount', (SELECT COUNT(*) FROM auction_bids WHERE lot_id=${lot} AND idempotency_key=${rejected}),
    'lotStatus', COALESCE((SELECT status FROM auction_lots WHERE id=${lot}), -1),
    'lotVersion', COALESCE((SELECT version FROM auction_lots WHERE id=${lot}), -1),
    'currentPrice', COALESCE((SELECT current_price_amount FROM auction_lots WHERE id=${lot}), -1),
    'projectionVersion', COALESCE((SELECT last_lot_version FROM auction_lot_projection_state WHERE lot_id=${lot}), -1),
    'maxInboxVersion', COALESCE((SELECT MAX(lot_version) FROM auction_projection_inbox WHERE lot_id=${lot}), -1),
    'domainPending', (SELECT COUNT(*) FROM auction_domain_outbox WHERE published_at_ms=0),
    'unresolvedHighFindings', (SELECT COUNT(*) FROM auction_reconcile_findings WHERE severity IN ('P0','P1') AND resolved_at_ms=0),
    'newHighFindings', (SELECT COUNT(*) FROM auction_reconcile_findings WHERE severity IN ('P0','P1') AND resolved_at_ms=0 AND detected_at_ms >= ${detectedAt})
  );`;
  return queryMySQLJSON(config, sql);
}

function queryMySQLJSON(config, sql) {
  const result = spawnSync('docker', [
    'compose', '-f', config.composeFile, 'exec', '-T', config.mysqlService,
    'sh', '-lc', 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --batch --raw --skip-column-names -u root --database="$MYSQL_DATABASE"',
  ], { input: `${sql}\n`, encoding: 'utf8', maxBuffer: 1024 * 1024 });
  if (result.error) throw new Error(`run MySQL evidence query: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`MySQL evidence query exited ${result.status}: ${String(result.stderr).trim()}`);
  const output = String(result.stdout).trim().split('\n').at(-1) || '';
  let parsed;
  try {
    parsed = JSON.parse(output);
  } catch {
    throw new Error(`MySQL evidence query returned invalid JSON: ${output.slice(0, 240)}`);
  }
  return Object.fromEntries(Object.entries(parsed).map(([key, value]) => [key, Number(value)]));
}

function evaluateAfterDatabase(database, state) {
  const failures = [];
  if (database.acceptedBidCount !== 1) failures.push(`accepted bid count=${database.acceptedBidCount} want=1`);
  if (database.rejectedBidCount !== 0) failures.push(`OVERLOADED bid projected ${database.rejectedBidCount} rows`);
  if (database.lotStatus !== LOT_STATUS_CANCELLED) failures.push(`lot status=${database.lotStatus} want=${LOT_STATUS_CANCELLED}`);
  if (database.currentPrice !== Number(state.acceptedAmount)) failures.push(`current price=${database.currentPrice} want=${state.acceptedAmount}`);
  if (database.lotVersion !== database.projectionVersion || database.lotVersion !== database.maxInboxVersion) {
    failures.push(`version chain lot=${database.lotVersion} projection=${database.projectionVersion} inbox=${database.maxInboxVersion}`);
  }
  if (database.newHighFindings !== 0) failures.push(`new unresolved P0/P1 findings=${database.newHighFindings}`);
  if (database.domainPending > Number(state.baselineDomainPending)) {
    failures.push(`domain outbox pending=${database.domainPending} baseline=${state.baselineDomainPending}`);
  }
  return failures;
}

function timelineItem(snapshot, admissionStatus) {
  return {
    observedAt: new Date().toISOString(), admissionStatus,
    ready: snapshot.ready, reason: snapshot.reason, totalLagRecords: snapshot.totalLagRecords,
    oldestAgeMs: snapshot.oldestAgeMs, consecutiveHealthyPolls: snapshot.consecutiveHealthyPolls,
    latestSum: partitionLatestSum(snapshot),
  };
}

async function readState(config) {
  let state;
  try {
    state = JSON.parse(await readFile(config.stateFile, 'utf8'));
  } catch (error) {
    throw new Error(`read projector assertion state ${config.stateFile}: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (state.schemaVersion !== 1 || state.scenario !== config.scenario || !state.lotId) {
    throw new Error('projector assertion state does not match this fault run');
  }
  return state;
}

async function writeJSON(file, value) {
  await writeFile(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600 });
}

function tokenFrom(reply) {
  return reply?.tokens?.accessToken || reply?.tokens?.access_token || '';
}

function money(amount) {
  return { amount, currency: 'CNY' };
}

function sqlQuote(value) {
  return `'${String(value).replaceAll('\\', '\\\\').replaceAll("'", "''")}'`;
}

function positiveInt(raw, fallback) {
  const value = Number.parseInt(String(raw || ''), 10);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function normalizeURL(value) {
  const url = String(value || '').trim().replace(/\/+$/, '');
  const parsed = new URL(url);
  if (!['http:', 'https:'].includes(parsed.protocol)) throw new Error(`unsupported URL protocol: ${parsed.protocol}`);
  return url;
}

function delay(ms) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms));
}

export {
  accountIdentity,
  apiRequest,
  bidAccepted,
  bidID,
  createLiveLot,
  delay,
  ensureAccount,
  ensureDepositHeld,
  evaluateAfterDatabase,
  firstAdminRoom,
  isHealthyProjection,
  loginAccount,
  normalizeProjectionSnapshot,
  normalizeURL,
  operationRequest,
  overloadedResult,
  partitionLatestSum,
  placeBid,
  positiveInt,
  projectionSnapshot,
  queryMySQLJSON,
  readDatabaseEvidence,
  sqlQuote,
  waitForProjection,
  writeJSON,
};
