#!/usr/bin/env node

import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const baseUrl = (process.env.BASE_URL || 'http://127.0.0.1:18080').replace(/\/+$/, '');
const metricsEndpoints = resolveMetricsEndpoints(process.env, baseUrl);
const concurrency = parsePositiveInt(process.env.CONCURRENCY, 100);
const targetBidRatePerSecond = parseNonNegativeNumber(process.env.TARGET_BID_RATE_PER_SECOND, 0);
const loadDurationSeconds = parsePositiveNumber(process.env.LOAD_DURATION_SECONDS, 60);
const bidRequestCount = targetBidRatePerSecond > 0
  ? Math.ceil(targetBidRatePerSecond * loadDurationSeconds)
  : concurrency;
const wsConnectionTarget = parseNonNegativeInt(process.env.WS_CONNECTIONS, 0);
const wsOpenTimeoutMs = parsePositiveInt(process.env.WS_OPEN_TIMEOUT_MS, 15_000);
const fanoutSettleTimeoutMs = parsePositiveInt(process.env.FANOUT_SETTLE_TIMEOUT_MS, 5_000);
const projectionDrainTimeoutMs = parsePositiveInt(process.env.PROJECTION_DRAIN_TIMEOUT_MS, 15_000);
const rankingLimit = parsePositiveInt(process.env.AUCTION_REALTIME_RANKING_LIMIT, 50);
const bidP99LimitMs = parsePositiveInt(process.env.BID_P99_LIMIT_MS, 200);
const minBidThroughputPerSecond = parsePositiveNumber(
  process.env.MIN_BID_THROUGHPUT_PER_SECOND,
  targetBidRatePerSecond > 0 ? targetBidRatePerSecond * 0.95 : 100,
);
const minOfferedRateRatio = parseRate(process.env.MIN_OFFERED_RATE_RATIO, 0.99);
const maxScheduleDriftP99Ms = parsePositiveNumber(process.env.MAX_SCHEDULE_DRIFT_P99_MS, 100);
const maxSystemErrorRate = parseRate(process.env.MAX_SYSTEM_ERROR_RATE, 0);
const runId = process.env.RUN_ID || `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
const reportFile = process.env.REPORT_FILE ? resolve(process.env.REPORT_FILE) : '';
const barrierReadyFile = process.env.LOAD_BARRIER_READY_FILE ? resolve(process.env.LOAD_BARRIER_READY_FILE) : '';
const barrierStartFile = process.env.LOAD_BARRIER_START_FILE ? resolve(process.env.LOAD_BARRIER_START_FILE) : '';
const barrierTimeoutMs = parsePositiveInt(process.env.LOAD_BARRIER_TIMEOUT_MS, 10 * 60_000);
const merchantUsername = process.env.MERCHANT_USERNAME || `load_main_${runId}`;
const merchantPassword = process.env.MERCHANT_PASSWORD || 'LoadTestPass123!';
const buyerPrefix = process.env.BUYER_PREFIX || `load_buyer_${runId}`;
const startPrice = parsePositiveInt(process.env.START_PRICE_CENTS, 10000);
const minIncrement = parsePositiveInt(process.env.MIN_INCREMENT_CENTS, 100);
const capPrice = startPrice + bidRequestCount * minIncrement;
let activeFanout = null;

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch((error) => {
    activeFanout?.close();
    console.error(error?.stack || error);
    process.exit(1);
  });
}

async function main() {
  validateMetricsEndpoints(metricsEndpoints, process.env.ALLOW_SHARED_WORKER_METRICS === '1');
  await verifyMetricsEndpoint(metricsEndpoints.gateway, 'gateway', 'go_build_info');
  await verifyMetricsEndpoint(metricsEndpoints.projector, 'projector', 'go_build_info');
  await verifyMetricsEndpoint(metricsEndpoints.relay, 'outbox-relay', 'go_build_info');

  const merchant = await ensureMerchant();
  const merchantToken = tokenOf(merchant);
  const room = await firstAdminRoom(merchantToken);
  const lot = await createQueuedStartedLot(merchantToken, room.id);

  await assertPublicRoomVisible(room.id, 'queued/start setup');

  const buyers = await registerBuyers(concurrency);
  for (const [index, buyer] of buyers.entries()) {
    await ensureDepositHeld(buyer, lot.id, `load-${runId}-${index}`);
  }
  const [gatewayMetricsBeforeConnections, projectorMetricsBeforeLoad, relayMetricsBeforeLoad] = await Promise.all([
    scrapeMetrics(metricsEndpoints.gateway, 'gateway'),
    scrapeMetrics(metricsEndpoints.projector, 'projector'),
    scrapeMetrics(metricsEndpoints.relay, 'outbox-relay'),
  ]);
  const fanout = await openFanoutClients(wsConnectionTarget, room.id);
  activeFanout = fanout;
  await delay(250);
  const gatewayMetricsAfterConnections = await scrapeMetrics(metricsEndpoints.gateway, 'gateway');
  const barrier = await waitForLoadBarrier();
  fanout?.startMeasurement();
  const bidBatchStartedAtUnixMs = Date.now();
  const bidBatchStartedAt = performance.now();
  const executeBid = (index) => {
    const buyer = buyers[index % buyers.length];
    const amount = startPrice + (index + 1) * minIncrement;
    const idempotencyKey = `load-bid-${runId}-${index}`;
    return timed(() => placeBid(buyer.token, lot.id, amount, idempotencyKey))
      .then((result) => ({ ...result, buyer, amount, idempotencyKey }))
      .catch((error) => ({ error, buyer, amount, idempotencyKey, ms: 0 }));
  };
  const bidResults = targetBidRatePerSecond > 0
    ? await runOpenArrivalBids(bidRequestCount, targetBidRatePerSecond, bidBatchStartedAt, executeBid)
    : await Promise.all(buyers.map((_buyer, index) => executeBid(index)));
  const bidBatchDurationMs = Math.round(performance.now() - bidBatchStartedAt);
  const bidBatchEndedAtUnixMs = Date.now();
  const accepted = bidResults.filter((item) => !item.error && item.reply.accepted);
  const rejected = bidResults.filter((item) => !item.error && !item.reply.accepted);
  const errors = bidResults.filter((item) => item.error);

  if (accepted.length === 0) {
    throw new Error(`no accepted bids; rejected=${rejected.length} errors=${errors.length}`);
  }

  const highest = accepted.reduce((best, item) => acceptedBidAmount(item) > acceptedBidAmount(best) ? item : best, accepted[0]);
  const highestAmount = acceptedBidAmount(highest);
  const duplicate = await placeBid(highest.buyer.token, lot.id, highestAmount, highest.idempotencyKey);
  if (duplicate.accepted && highest.reply.bid?.id && duplicate.bid?.id && duplicate.bid.id !== highest.reply.bid.id) {
    throw new Error(`idempotency replay created a different bid: first=${highest.reply.bid.id} replay=${duplicate.bid.id}`);
  }

  // Kafka's high watermark can still be zero while Relay has not produced this
  // run's facts. Drain Redis Outbox first, then evaluate Projector lag.
  const runtimeOutbox = await waitForRuntimeOutboxDrain(projectionDrainTimeoutMs);
  const projection = await waitForProjectionDrain(projectionDrainTimeoutMs);
  const snapshot = await getSnapshot(room.id);
  const currentLot = snapshot.currentLot || snapshot.current_lot;
  const ranking = highest.reply.ranking?.length ? highest.reply.ranking : (snapshot.ranking || []);
  const winnerResult = await lotResult(highest.buyer.token, lot.id);
  const resultLot = winnerResult.lot || {};
  const finalPrice = moneyAmount(
    resultLot.finalPrice || resultLot.final_price ||
    currentLot?.finalPrice || currentLot?.final_price || currentLot?.currentPrice || currentLot?.current_price,
  );
  const leader = resultLot.winnerUserId || resultLot.winner_user_id || currentLot?.winnerUserId || currentLot?.winner_user_id || currentLot?.leadingUserId || currentLot?.leading_user_id;
  if (finalPrice !== highestAmount) {
    throw new Error(`final/current price mismatch: got=${finalPrice} want=${highestAmount}`);
  }
  if (leader !== highest.buyer.user.id) {
    throw new Error(`leader mismatch: got=${leader} want=${highest.buyer.user.id}`);
  }
  assertRanking(ranking, rankingLimit);

  let orderCount = 0;
  if (finalPrice >= capPrice) {
    if (winnerResult.order || winnerResult.orderId || winnerResult.order_id) orderCount = 1;
    if (orderCount !== 1) throw new Error(`cap settlement should create one visible winner order, got=${orderCount}`);
  }

  await fanout?.waitForCoverage(fanoutSettleTimeoutMs);
  const [gatewayMetricsAfterLoad, projectorMetricsAfterLoad, relayMetricsAfterLoad] = await Promise.all([
    scrapeMetrics(metricsEndpoints.gateway, 'gateway'),
    scrapeMetrics(metricsEndpoints.projector, 'projector'),
    scrapeMetrics(metricsEndpoints.relay, 'outbox-relay'),
  ]);
  requireMetricFamily(projectorMetricsAfterLoad, 'auction_projection_lag_records', 'projector');
  requireMetricFamily(relayMetricsAfterLoad, 'auction_outbox_pending', 'outbox-relay');
  requireMetricFamily(relayMetricsAfterLoad, 'auction_outbox_inflight', 'outbox-relay');
  requireMetricFamily(relayMetricsAfterLoad, 'auction_outbox_ack_result_total', 'outbox-relay');

  const latencies = bidResults.filter((item) => Number.isFinite(item.ms) && item.ms > 0).map((item) => item.ms).sort((a, b) => a - b);
  const systemErrors = errors.length;
  const errorRate = bidResults.length ? systemErrors / bidResults.length : 1;
  const throughput = bidBatchDurationMs > 0 ? bidResults.length / (bidBatchDurationMs / 1000) : 0;
  const p99Ms = percentile(latencies, 99);
  const offeredRatePerSecond = calculateOfferedRate(bidResults);
  const scheduleDrifts = bidResults
    .map((item) => item.scheduleDriftMs)
    .filter((value) => Number.isFinite(value))
    .sort((left, right) => left - right);
  const scheduleDriftP99Ms = percentile(scheduleDrifts, 99);
  const fanoutReport = fanout?.report() || emptyFanoutReport();
  const thresholds = {
    bidP99LimitMs,
    minBidThroughputPerSecond,
    maxSystemErrorRate,
    targetBidRatePerSecond,
    minOfferedRateRatio,
    maxScheduleDriftP99Ms,
  };
  const failures = evaluateBidThresholds({
    total: bidResults.length,
    systemErrors,
    errorRate,
    throughputPerSecond: throughput,
    p99Ms,
    offeredRatePerSecond,
    scheduleDriftP99Ms,
  }, thresholds);
  if (wsConnectionTarget > 0 && fanoutReport.connectionsWithEvent !== fanoutReport.connected) {
    failures.push(`fanout coverage: ${fanoutReport.connectionsWithEvent}/${fanoutReport.connected}`);
  }
  const finalProjectionLag = metricValue(projectorMetricsAfterLoad, 'auction_projection_lag_records');
  if (projection.pendingCount !== 0 || finalProjectionLag !== 0) {
    failures.push(`projection pending after timeout: waited=${projection.pendingCount} final=${finalProjectionLag}`);
  }
  if (runtimeOutbox.pendingCount !== 0 || runtimeOutbox.inflightCount !== 0) {
    failures.push(`runtime outbox not drained: pending=${runtimeOutbox.pendingCount} inflight=${runtimeOutbox.inflightCount}`);
  }
  if (metricValue(projectorMetricsAfterLoad, 'auction_projection_paused') > 0) failures.push('projector partition paused');
  const outboxAckDelta = counterDelta(
    relayMetricsBeforeLoad,
    relayMetricsAfterLoad,
    'auction_outbox_ack_result_total',
    { result: 'ok' },
  );
  if (outboxAckDelta < accepted.length) {
    failures.push(`runtime outbox ACK count ${outboxAckDelta} is below accepted bid count ${accepted.length}`);
  }
  const outboxInvariantFailureDelta = ['empty', 'malformed', 'mismatch'].reduce(
    (total, result) => total + counterDelta(
      relayMetricsBeforeLoad,
      relayMetricsAfterLoad,
      'auction_outbox_ack_result_total',
      { result },
    ),
    0,
  );
  if (outboxInvariantFailureDelta > 0) failures.push(`runtime outbox invariant failures increased: ${outboxInvariantFailureDelta}`);
  if (counterDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterLoad, 'auction_ws_events_dropped_total') > 0) {
    failures.push('websocket dropped events increased');
  }
  const report = {
    schemaVersion: 1,
    status: failures.length ? 'FAIL' : 'PASS',
    generatedAt: new Date().toISOString(),
    baseUrl,
    metricsEndpoints,
    runId,
    roomId: room.id,
    lotId: lot.id,
    concurrency,
    bidderPoolSize: buyers.length,
    loadProfile: {
      mode: targetBidRatePerSecond > 0 ? 'open-arrival-rate' : 'concurrent-burst',
      targetBidRatePerSecond,
      loadDurationSeconds: targetBidRatePerSecond > 0 ? loadDurationSeconds : 0,
      offeredRatePerSecond: round(offeredRatePerSecond, 2),
      scheduleDriftP99Ms,
      barrier,
    },
    total: bidResults.length,
    accepted: accepted.length,
    rejected: rejected.length,
    systemErrors,
    errorRate,
    bidBatchDurationMs,
    bidBatchStartedAtUnixMs,
    bidBatchEndedAtUnixMs,
    throughputPerSecond: round(throughput, 2),
    p50Ms: percentile(latencies, 50),
    p95Ms: percentile(latencies, 95),
    p99Ms,
    thresholds,
    finalPrice,
    leader,
    highestAcceptedBidder: highest.buyer.user.id,
    rankingLength: ranking.length,
    rankingLimit,
    orderCount,
    websocket: fanoutReport,
    connectionMemory: {
      heapAllocBeforeBytes: metricValue(gatewayMetricsBeforeConnections, 'go_memstats_heap_alloc_bytes'),
      heapAllocAfterOpenBytes: metricValue(gatewayMetricsAfterConnections, 'go_memstats_heap_alloc_bytes'),
      heapAllocDeltaBytes: metricDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterConnections, 'go_memstats_heap_alloc_bytes'),
      heapAllocPerConnectionBytes: perConnectionDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterConnections, 'go_memstats_heap_alloc_bytes', fanoutReport.connected),
      residentBeforeBytes: metricValue(gatewayMetricsBeforeConnections, 'process_resident_memory_bytes'),
      residentAfterOpenBytes: metricValue(gatewayMetricsAfterConnections, 'process_resident_memory_bytes'),
      residentDeltaBytes: metricDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterConnections, 'process_resident_memory_bytes'),
      residentPerConnectionBytes: perConnectionDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterConnections, 'process_resident_memory_bytes', fanoutReport.connected),
    },
    projection: {
      ...projection,
      lagRecords: finalProjectionLag,
      retryDelta: counterDelta(projectorMetricsBeforeLoad, projectorMetricsAfterLoad, 'auction_projection_retry_total'),
      duplicateDelta: counterDelta(projectorMetricsBeforeLoad, projectorMetricsAfterLoad, 'auction_projection_duplicate_total'),
    },
    runtimeOutbox: {
      ...runtimeOutbox,
      ackDelta: outboxAckDelta,
      invariantFailureDelta: outboxInvariantFailureDelta,
    },
    serverFanoutCounters: {
      sentDelta: counterDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterLoad, 'auction_ws_events_sent_total'),
      coalescedDelta: counterDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterLoad, 'auction_ws_events_coalesced_total'),
      droppedDelta: counterDelta(gatewayMetricsBeforeConnections, gatewayMetricsAfterLoad, 'auction_ws_events_dropped_total'),
    },
    failures,
  };
  const output = `${JSON.stringify(report, null, 2)}\n`;
  if (reportFile) {
    await mkdir(dirname(reportFile), { recursive: true });
    await writeFile(reportFile, output, 'utf8');
  }
  console.log(output.trimEnd());
  fanout?.close();
  activeFanout = null;
  if (failures.length) throw new Error(`baseline assertions failed: ${failures.join('; ')}`);
}

async function openFanoutClients(count, roomId) {
  if (count === 0) return null;
  if (typeof WebSocket !== 'function') {
    throw new Error('WS_CONNECTIONS requires a Node.js runtime with global WebSocket support');
  }
  const wsBaseUrl = baseUrl.replace(/^http:/, 'ws:').replace(/^https:/, 'wss:');
  const measurement = { startedAt: 0 };
  const attempts = await Promise.allSettled(Array.from({ length: count }, (_, index) => (
    openFanoutClient(`${wsBaseUrl}/ws/rooms/${encodeURIComponent(roomId)}?scope=public`, index, measurement)
  )));
  const clients = attempts.filter((item) => item.status === 'fulfilled').map((item) => item.value);
  const failures = attempts.filter((item) => item.status === 'rejected');
  if (failures.length) {
    clients.forEach((client) => client.socket.close());
    throw new Error(`failed to open ${failures.length}/${count} websocket connections: ${failures[0].reason}`);
  }
  return {
    startMeasurement() {
      measurement.startedAt = performance.now();
      clients.forEach((client) => {
        client.firstEventLatencyMs = null;
        client.messageCount = 0;
      });
    },
    async waitForCoverage(timeoutMs) {
      const deadline = Date.now() + timeoutMs;
      while (Date.now() < deadline && clients.some((client) => client.firstEventLatencyMs === null)) {
        await delay(25);
      }
    },
    report() {
      const latencies = clients
        .map((client) => client.firstEventLatencyMs)
        .filter((value) => Number.isFinite(value))
        .sort((a, b) => a - b);
      const connectionsWithEvent = latencies.length;
      return {
        requested: count,
        connected: clients.length,
        connectionsWithEvent,
        coverageRate: clients.length ? connectionsWithEvent / clients.length : 0,
        messagesReceived: clients.reduce((total, client) => total + client.messageCount, 0),
        firstEventP50Ms: percentile(latencies, 50),
        firstEventP95Ms: percentile(latencies, 95),
        firstEventP99Ms: percentile(latencies, 99),
      };
    },
    close() {
      clients.forEach((client) => client.socket.close());
    },
  };
}

async function runOpenArrivalBids(count, ratePerSecond, batchStartedAt, executeBid) {
  const intervalMs = 1000 / ratePerSecond;
  return Promise.all(Array.from({ length: count }, async (_unused, index) => {
    const scheduledOffsetMs = index * intervalMs;
    const scheduledAt = batchStartedAt + scheduledOffsetMs;
    while (performance.now() < scheduledAt) {
      await delay(Math.max(1, scheduledAt - performance.now()));
    }
    const startedOffsetMs = performance.now() - batchStartedAt;
    const result = await executeBid(index);
    return {
      ...result,
      scheduledOffsetMs: round(scheduledOffsetMs, 3),
      startedOffsetMs: round(startedOffsetMs, 3),
      scheduleDriftMs: round(Math.max(0, startedOffsetMs - scheduledOffsetMs), 3),
    };
  }));
}

async function waitForLoadBarrier(options = {}) {
  const readyFile = options.readyFile ?? barrierReadyFile;
  const startFile = options.startFile ?? barrierStartFile;
  const timeoutMs = options.timeoutMs ?? barrierTimeoutMs;
  const barrierRunId = options.runId ?? runId;
  if (!readyFile && !startFile) return null;
  if (!readyFile || !startFile) {
    throw new Error('LOAD_BARRIER_READY_FILE and LOAD_BARRIER_START_FILE must be configured together');
  }
  await mkdir(dirname(readyFile), { recursive: true });
  await writeFile(readyFile, `${JSON.stringify({ runId: barrierRunId, readyAt: new Date().toISOString() })}\n`, {
    encoding: 'utf8',
    flag: 'wx',
  });
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const instruction = JSON.parse(await readFile(startFile, 'utf8'));
      const startAtUnixMs = Number(instruction.startAtUnixMs);
      if (!Number.isFinite(startAtUnixMs) || startAtUnixMs <= 0) {
        throw new Error('load barrier startAtUnixMs must be a positive number');
      }
      if (startAtUnixMs < Date.now() - 1000) {
        throw new Error(`load barrier start time is stale: ${startAtUnixMs}`);
      }
      const waitMs = startAtUnixMs - Date.now();
      if (waitMs > 0) await delay(waitMs);
      return { coordinated: true, startAtUnixMs };
    } catch (error) {
      if (error?.code !== 'ENOENT') throw error;
    }
    await delay(50);
  }
  throw new Error(`load barrier timed out after ${timeoutMs}ms`);
}

function openFanoutClient(url, index, measurement) {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url);
    const client = { socket, index, firstEventLatencyMs: null, messageCount: 0 };
    const timeout = setTimeout(() => {
      socket.close();
      reject(new Error(`websocket ${index} open timeout after ${wsOpenTimeoutMs}ms`));
    }, wsOpenTimeoutMs);
    socket.addEventListener('open', () => {
      clearTimeout(timeout);
      resolve(client);
    }, { once: true });
    socket.addEventListener('error', () => {
      clearTimeout(timeout);
      reject(new Error(`websocket ${index} failed to open`));
    }, { once: true });
    socket.addEventListener('message', () => {
      if (measurement.startedAt <= 0) return;
      client.messageCount += 1;
      if (client.firstEventLatencyMs === null) {
        client.firstEventLatencyMs = Math.round(performance.now() - measurement.startedAt);
      }
    });
  });
}

async function waitForProjectionDrain(timeoutMs) {
  const startedAt = performance.now();
  const deadline = Date.now() + timeoutMs;
  await delay(100);
  let pendingCount = 0;
  do {
    const metrics = await scrapeMetrics(metricsEndpoints.projector, 'projector');
    requireMetricFamily(metrics, 'auction_projection_lag_records', 'projector');
    pendingCount = metricValue(metrics, 'auction_projection_lag_records');
    if (pendingCount === 0) {
      return { pendingCount, catchupMs: Math.round(performance.now() - startedAt), timedOut: false };
    }
    await delay(50);
  } while (Date.now() < deadline);
  return { pendingCount, catchupMs: Math.round(performance.now() - startedAt), timedOut: true };
}

async function waitForRuntimeOutboxDrain(timeoutMs) {
  const startedAt = performance.now();
  const deadline = Date.now() + timeoutMs;
  await delay(100);
  let pendingCount = 0;
  let inflightCount = 0;
  do {
    const metrics = await scrapeMetrics(metricsEndpoints.relay, 'outbox-relay');
    requireMetricFamily(metrics, 'auction_outbox_pending', 'outbox-relay');
    requireMetricFamily(metrics, 'auction_outbox_inflight', 'outbox-relay');
    pendingCount = metricValue(metrics, 'auction_outbox_pending');
    inflightCount = metricValue(metrics, 'auction_outbox_inflight');
    if (pendingCount === 0 && inflightCount === 0) {
      return {
        pendingCount,
        inflightCount,
        catchupMs: Math.round(performance.now() - startedAt),
        timedOut: false,
      };
    }
    await delay(50);
  } while (Date.now() < deadline);
  return {
    pendingCount,
    inflightCount,
    catchupMs: Math.round(performance.now() - startedAt),
    timedOut: true,
  };
}

async function scrapeMetrics(url, component) {
  let response;
  try {
    response = await fetch(url, { signal: AbortSignal.timeout(5_000) });
  } catch (error) {
    throw new Error(`GET ${component} metrics ${url} failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (!response.ok) throw new Error(`GET ${component} metrics ${url} HTTP ${response.status}`);
  return response.text();
}

async function verifyMetricsEndpoint(url, component, requiredMetric) {
  const metrics = await scrapeMetrics(url, component);
  requireMetricFamily(metrics, requiredMetric, component);
}

function metricValue(metrics, name, requiredLabels = {}) {
  return metrics.split('\n').reduce((total, line) => {
    if (!line.startsWith(name)) return total;
    const separator = line[name.length];
    if (separator !== ' ' && separator !== '\t' && separator !== '{') return total;
    const labels = metricLabels(line.slice(name.length));
    if (Object.entries(requiredLabels).some(([key, value]) => labels[key] !== value)) return total;
    const value = Number(line.trim().split(/\s+/).at(-1));
    return Number.isFinite(value) ? total + value : total;
  }, 0);
}

function metricLabels(suffix) {
  if (!suffix.startsWith('{')) return {};
  const closing = suffix.indexOf('}');
  if (closing < 0) return {};
  const labels = {};
  const raw = suffix.slice(1, closing);
  const pattern = /([a-zA-Z_][a-zA-Z0-9_]*)="((?:\\.|[^"\\])*)"/g;
  for (const match of raw.matchAll(pattern)) {
    labels[match[1]] = match[2].replace(/\\n/g, '\n').replace(/\\"/g, '"').replace(/\\\\/g, '\\');
  }
  return labels;
}

function metricDelta(before, after, name, requiredLabels = {}) {
  return metricValue(after, name, requiredLabels) - metricValue(before, name, requiredLabels);
}

function counterDelta(before, after, name, requiredLabels = {}) {
  return Math.max(0, metricDelta(before, after, name, requiredLabels));
}

function perConnectionDelta(before, after, name, connections) {
  if (connections <= 0) return 0;
  return Math.round(metricDelta(before, after, name) / connections);
}

function hasMetricFamily(metrics, name) {
  return metrics.split('\n').some((line) => (
    line.startsWith(`# HELP ${name} `) ||
    line.startsWith(`# TYPE ${name} `) ||
    (line.startsWith(name) && [' ', '\t', '{'].includes(line[name.length]))
  ));
}

function requireMetricFamily(metrics, name, component) {
  if (!hasMetricFamily(metrics, name)) {
    throw new Error(`${component} metrics endpoint does not expose required family ${name}`);
  }
}

function resolveMetricsEndpoints(env, apiBaseUrl) {
  return {
    gateway: normalizeMetricsUrl(env.GATEWAY_METRICS_URL || `${apiBaseUrl}/metrics`),
    projector: normalizeMetricsUrl(env.PROJECTOR_METRICS_URL || ''),
    relay: normalizeMetricsUrl(env.RELAY_METRICS_URL || ''),
  };
}

function normalizeMetricsUrl(value) {
  return String(value || '').trim().replace(/\/+$/, '');
}

function validateMetricsEndpoints(endpoints, allowShared) {
  for (const [component, url] of Object.entries(endpoints)) {
    if (!url) {
      const variable = component === 'projector' ? 'PROJECTOR_METRICS_URL' : 'RELAY_METRICS_URL';
      throw new Error(`${variable} is required so ${component} health is read from its own process`);
    }
    let parsed;
    try {
      parsed = new URL(url);
    } catch {
      throw new Error(`invalid ${component} metrics URL: ${url}`);
    }
    if (!['http:', 'https:'].includes(parsed.protocol)) {
      throw new Error(`invalid ${component} metrics protocol: ${parsed.protocol}`);
    }
  }
  if (!allowShared && (endpoints.projector === endpoints.gateway || endpoints.relay === endpoints.gateway)) {
    throw new Error('worker metrics endpoints must be distinct from Gateway; set ALLOW_SHARED_WORKER_METRICS=1 only for an intentional single-process deployment');
  }
}

function emptyFanoutReport() {
  return {
    requested: 0,
    connected: 0,
    connectionsWithEvent: 0,
    coverageRate: 0,
    messagesReceived: 0,
    firstEventP50Ms: 0,
    firstEventP95Ms: 0,
    firstEventP99Ms: 0,
  };
}

function delay(ms) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms));
}

async function ensureMerchant() {
  const body = { username: merchantUsername, password: merchantPassword, nickname: merchantUsername };
  try {
    return await request('/api/merchants/register', { method: 'POST', body });
  } catch (error) {
    if (String(error?.message || error).includes('network failed')) throw error;
    return request('/api/users/login', { method: 'POST', body: { username: merchantUsername, password: merchantPassword } });
  }
}

async function firstAdminRoom(token) {
  const reply = await request('/api/admin/rooms', { token });
  const rooms = reply.rooms || [];
  if (!rooms.length) throw new Error('admin room list is empty after merchant login');
  return normalizeRoom(rooms[0]);
}

async function createQueuedStartedLot(token, roomId) {
  const create = await request('/api/lots', {
    method: 'POST',
    token,
    body: {
      room_id: roomId,
      title: `Load hot path ${runId}`,
      description: 'repeatable concurrent bid hot-path smoke',
      image_url: 'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/live-anchor-01.jpg',
      rule: {
        start_price: money(startPrice),
        min_increment: money(minIncrement),
        duration_seconds: 300,
        anti_snipe_window_seconds: 15,
        anti_snipe_extend_seconds: 15,
        max_extend_count: 3,
        cap_price: money(capPrice),
      },
    },
  });
  const lot = normalizeLot(create.lot);
  await request(`/api/lots/${encodeURIComponent(lot.id)}/queue`, { method: 'POST', token, body: {} });
  await assertPublicRoomVisible(roomId, 'queued lot');
  const started = await request(`/api/lots/${encodeURIComponent(lot.id)}/start`, { method: 'POST', token, body: {} });
  return normalizeLot(started.lot || lot);
}

async function assertPublicRoomVisible(roomId, stage) {
  const reply = await request('/api/rooms');
  const rooms = (reply.rooms || []).map(normalizeRoom);
  if (!rooms.some((room) => room.id === roomId)) {
    throw new Error(`public room ${roomId} is not visible after ${stage}`);
  }
}

async function registerBuyers(count) {
  const buyers = [];
  for (let index = 0; index < count; index += 1) {
    const username = `${buyerPrefix}_${index}`;
    const password = 'BuyerPass123!';
    let reply;
    try {
      reply = await request('/api/users/register', {
        method: 'POST',
        body: { username, password, nickname: `买家${index}` },
      });
    } catch (error) {
      if (String(error?.message || error).includes('network failed')) throw error;
      reply = await request('/api/users/login', { method: 'POST', body: { username, password } });
    }
    buyers.push({ user: normalizeUser(reply.user), token: tokenOf(reply), username, addressId: '' });
  }
  return buyers;
}

async function createDeliveryAddress(buyer) {
  const reply = await request('/api/shop/addresses', {
    method: 'POST',
    token: buyer.token,
    body: {
      address: {
        receiverName: `Load ${buyer.username}`,
        phone: '13800138000',
        province: '上海市',
        city: '上海市',
        district: '浦东新区',
        street: '世纪大道',
        detail: `Load-test address for ${buyer.username}`,
        tag: 'load',
        isDefault: true,
      },
    },
  });
  const addressId = reply.address?.id || reply.address?.address_id || '';
  if (!addressId) throw new Error(`delivery address missing id for buyer ${buyer.username}`);
  buyer.addressId = addressId;
  return addressId;
}

async function ensureDepositHeld(buyer, lotId, scope) {
  const addressId = buyer.addressId || await createDeliveryAddress(buyer);
  const idempotencyKey = `${scope}-deposit`;
  const reply = await request(`/api/lots/${encodeURIComponent(lotId)}/deposit-holds/mock-pay`, {
    method: 'POST',
    token: buyer.token,
    idempotencyKey,
    body: { addressId, idempotencyKey },
  });
  if (!reply.paid) throw new Error(`deposit hold was not paid for buyer ${buyer.username} lot ${lotId}`);
  return reply;
}

async function placeBid(token, lotId, amount, idempotencyKey) {
  const reply = await request(`/api/lots/${encodeURIComponent(lotId)}/bid`, {
    method: 'POST',
    token,
    idempotencyKey,
    allowResultError: true,
    body: {
      amount: money(amount),
      idempotency_key: idempotencyKey,
    },
  });
  const bidAmount = moneyAmount(reply.bid?.amount);
  return {
    accepted: Boolean(reply.accepted || (reply.bid?.id && bidAmount > 0)),
    bid: reply.bid,
    ranking: reply.ranking || [],
    rejectReason: reply.rejectReason || reply.reject_reason || reply.result?.message || '',
  };
}

async function getSnapshot(roomId) {
  const reply = await request(`/api/rooms/${encodeURIComponent(roomId)}/snapshot`);
  return reply.snapshot || reply;
}

async function lotResult(token, lotId) {
  return request(`/api/lots/${encodeURIComponent(lotId)}/result`, { token });
}

async function timed(fn) {
  const started = performance.now();
  const reply = await fn();
  return { reply, ms: Math.round(performance.now() - started) };
}

async function request(path, options = {}) {
  const headers = new Headers({ Accept: 'application/json' });
  if (options.body !== undefined) headers.set('Content-Type', 'application/json');
  if (options.token) headers.set('Authorization', `Bearer ${options.token}`);
  if (options.idempotencyKey) headers.set('Idempotency-Key', options.idempotencyKey);
  const method = options.method || 'GET';
  let response;
  try {
    response = await fetch(`${baseUrl}${path}`, {
      method,
      headers,
      body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
    });
  } catch (error) {
    throw new Error(`${method} ${baseUrl}${path} network failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  const text = await response.text();
  let data = {};
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      throw new Error(`${method} ${path} returned non-json ${response.status}: ${text.slice(0, 160)}`);
    }
  }
  if (!response.ok) throw new Error(`${method} ${path} HTTP ${response.status}: ${text.slice(0, 240)}`);
  const result = data.result;
  if (result && Number(result.code || 0) !== 0 && !options.allowResultError) {
    throw new Error(`${method} ${path} failed: ${result.message || result.code}`);
  }
  return data;
}

function assertRanking(ranking, limit) {
  if (ranking.length > limit) throw new Error(`ranking length ${ranking.length} exceeds limit ${limit}`);
  for (let index = 1; index < ranking.length; index += 1) {
    const previous = moneyAmount(ranking[index - 1].amount);
    const current = moneyAmount(ranking[index].amount);
    if (previous < current) throw new Error(`ranking is not sorted at ${index}: ${previous} < ${current}`);
  }
}

function percentile(values, p) {
  if (!values.length) return 0;
  const index = Math.min(values.length - 1, Math.ceil((p / 100) * values.length) - 1);
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

function parsePositiveNumber(raw, fallback) {
  const value = Number.parseFloat(String(raw || ''));
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function parseNonNegativeNumber(raw, fallback) {
  const value = Number.parseFloat(String(raw || ''));
  return Number.isFinite(value) && value >= 0 ? value : fallback;
}

function parseRate(raw, fallback) {
  const value = Number.parseFloat(String(raw || ''));
  return Number.isFinite(value) && value >= 0 && value <= 1 ? value : fallback;
}

function evaluateBidThresholds(result, thresholds) {
  const failures = [];
  if (result.errorRate > thresholds.maxSystemErrorRate) {
    failures.push(`system error rate ${round(result.errorRate, 6)} exceeds ${thresholds.maxSystemErrorRate} (${result.systemErrors}/${result.total})`);
  }
  if (result.p99Ms >= thresholds.bidP99LimitMs) {
    failures.push(`bid p99 ${result.p99Ms}ms must be below ${thresholds.bidP99LimitMs}ms`);
  }
  if (result.throughputPerSecond < thresholds.minBidThroughputPerSecond) {
    failures.push(`bid throughput ${round(result.throughputPerSecond, 2)}/s is below ${thresholds.minBidThroughputPerSecond}/s`);
  }
  if (thresholds.targetBidRatePerSecond > 0) {
    const minimumOfferedRate = thresholds.targetBidRatePerSecond * thresholds.minOfferedRateRatio;
    if (result.offeredRatePerSecond < minimumOfferedRate) {
      failures.push(`offered bid rate ${round(result.offeredRatePerSecond, 2)}/s is below ${round(minimumOfferedRate, 2)}/s`);
    }
    if (result.scheduleDriftP99Ms > thresholds.maxScheduleDriftP99Ms) {
      failures.push(`load-generator schedule drift p99 ${result.scheduleDriftP99Ms}ms exceeds ${thresholds.maxScheduleDriftP99Ms}ms`);
    }
  }
  return failures;
}

function calculateOfferedRate(results) {
  const starts = results
    .map((item) => item.startedOffsetMs)
    .filter((value) => Number.isFinite(value))
    .sort((left, right) => left - right);
  if (starts.length < 2) return 0;
  const elapsedMs = starts.at(-1) - starts[0];
  return elapsedMs > 0 ? (starts.length - 1) / (elapsedMs / 1000) : 0;
}

function round(value, digits) {
  const factor = 10 ** digits;
  return Math.round(value * factor) / factor;
}

function money(amount) {
  return { amount, currency: 'CNY' };
}

function moneyAmount(value) {
  if (!value) return 0;
  return Number(value.amount || 0);
}

function acceptedBidAmount(item) {
  return moneyAmount(item?.reply?.bid?.amount) || item?.amount || 0;
}

function tokenOf(reply) {
  const tokens = reply.tokens || {};
  const token = tokens.accessToken || tokens.access_token;
  if (!token) throw new Error('missing access token');
  return token;
}

function normalizeRoom(room) {
  return { ...room, id: room?.id || room?.room_id || '' };
}

function normalizeLot(lot) {
  return { ...lot, id: lot?.id || lot?.lot_id || '', roomId: lot?.roomId || lot?.room_id || '' };
}

function normalizeUser(user) {
  return { ...user, id: user?.id || user?.user_id || '' };
}

export {
  counterDelta,
  calculateOfferedRate,
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
};
