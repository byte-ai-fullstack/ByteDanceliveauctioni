import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

import {
  ackCounters,
  ackFailureTotal,
  evaluateRelayDatabase,
  redisCLIQuote,
} from './assert-relay-recovery.mjs';

test('ACK evidence counts only invariant failures', () => {
  const metrics = [
    'auction_outbox_ack_result_total{result="ok"} 12',
    'auction_outbox_ack_result_total{result="mismatch"} 1',
    'auction_outbox_ack_result_total{result="malformed"} 2',
    'auction_outbox_ack_result_total{result="error"} 3',
  ].join('\n');
  const counters = ackCounters(metrics);
  assert.equal(counters.ok, 12);
  assert.equal(ackFailureTotal(counters), 6);
});

test('Relay MySQL evidence requires bid uniqueness and a gap-free version chain', () => {
  const state = { bidCount: 4, baselineAmount: 10_100, finalAmount: 10_500, baselineDomainPending: 0 };
  const passing = {
    acceptedBidCount: 4,
    distinctIdempotencyCount: 4,
    minimumBidAmount: 10_200,
    maximumBidAmount: 10_500,
    lotStatus: 2,
    currentPrice: 10_500,
    lotVersion: 6,
    projectionVersion: 6,
    inboxMinimumVersion: 1,
    inboxMaximumVersion: 6,
    inboxDistinctVersions: 6,
    domainPending: 0,
    newHighFindings: 0,
  };
  assert.deepEqual(evaluateRelayDatabase(passing, state), []);
  const failures = evaluateRelayDatabase({
    ...passing,
    acceptedBidCount: 3,
    distinctIdempotencyCount: 3,
    projectionVersion: 5,
    inboxDistinctVersions: 5,
    newHighFindings: 1,
  }, state);
  assert.equal(failures.length, 5);
});

test('Redis credentials stay on stdin and are safely quoted for redis-cli', () => {
  assert.equal(redisCLIQuote('secret "line"\\next\n'), '"secret \\"line\\"\\\\next\\n"');
});

test('CLI proves Relay accumulation, fenced takeover and projection continuity', async (t) => {
  const scratch = await mkdtemp(join(tmpdir(), 'relay-assertion-'));
  t.after(() => rm(scratch, { recursive: true, force: true }));
  const resultDir = join(scratch, 'results');
  const binDir = join(scratch, 'bin');
  await mkdir(resultDir, { recursive: true });
  await mkdir(binDir, { recursive: true });
  const composeFile = join(scratch, 'compose.yml');
  await writeFile(composeFile, 'services: {}\n');
  await writeFile(join(binDir, 'docker'), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *redis-cli* ]]; then
  /usr/bin/cat >/dev/null
  case "\${FAULT_PHASE:-}" in
    before) printf '%s\\n' '{"pending":0,"inflight":0,"ownerCount":16,"epochSum":16}' ;;
    during) printf '%s\\n' '{"pending":4,"inflight":0,"ownerCount":16,"epochSum":16}' ;;
    after) printf '%s\\n' '{"pending":0,"inflight":0,"ownerCount":16,"epochSum":32}' ;;
  esac
  exit 0
fi
/usr/bin/cat >/dev/null
if [[ "\${FAULT_PHASE:-}" == "before" ]]; then
  printf '%s\\n' '{"databaseNowMs":1000,"acceptedBidCount":1,"rejectedBidCount":0,"lotStatus":2,"lotVersion":2,"currentPrice":10100,"projectionVersion":2,"maxInboxVersion":2,"domainPending":0,"unresolvedHighFindings":0,"newHighFindings":0}'
else
  printf '%s\\n' '{"acceptedBidCount":4,"distinctIdempotencyCount":4,"minimumBidAmount":10200,"maximumBidAmount":10500,"lotStatus":2,"currentPrice":10500,"lotVersion":6,"projectionVersion":6,"inboxMinimumVersion":1,"inboxMaximumVersion":6,"inboxDistinctVersions":6,"domainPending":0,"newHighFindings":0}'
fi
`, { mode: 0o755 });

  let phase = 'before';
  let lastProjectionReason = 'healthy';
  const tokenAddresses = new Map();
  const depositReadyTokens = new Set();
  const server = http.createServer(async (request, response) => {
    const body = await readRequestJSON(request);
    const auth = request.headers.authorization || '';
    const token = auth.startsWith('Bearer ') ? auth.slice('Bearer '.length) : '';
    const send = (status, payload, contentType = 'application/json') => {
      response.writeHead(status, { 'Content-Type': contentType });
      response.end(contentType === 'application/json' ? `${JSON.stringify(payload)}\n` : payload);
    };
    if (request.url === '/readyz') return send(200, { ok: true });
    if (request.url === '/admissionz') return send(200, { status: 'open' });
    if (request.url === '/metrics/runtime-projection') {
      const latest = phase === 'after' ? 9 : 5;
      lastProjectionReason = 'healthy';
      return send(200, projectionPayload(latest));
    }
    if (request.url === '/relay-metrics') {
      const ok = phase === 'after' ? 4 : 1;
      return send(200, [
        `auction_outbox_ack_result_total{result="ok"} ${ok}`,
        'auction_outbox_ack_result_total{result="mismatch"} 0',
        'auction_outbox_ack_result_total{result="malformed"} 0',
        'auction_outbox_ack_result_total{result="empty"} 0',
        'auction_outbox_ack_result_total{result="not_owner"} 0',
        'auction_outbox_ack_result_total{result="error"} 0',
        'auction_outbox_owner{shard="all"} 16',
        '',
      ].join('\n'), 'text/plain');
    }
    if (request.url === '/api/admin/rooms') return send(200, { result: { code: 0 }, rooms: [{ id: 'room-1' }] });
    if (request.url === '/api/lots' && request.method === 'POST') return send(200, { result: { code: 0 }, lot: { id: 'lot-1', room_id: body.room_id } });
    if (request.url === '/api/lots/lot-1/queue') return send(200, { result: { code: 0 }, lot: { id: 'lot-1' } });
    if (request.url === '/api/lots/lot-1/start') return send(200, { result: { code: 0 }, lot: { id: 'lot-1' } });
    if (request.url === '/api/shop/addresses') {
      const nextId = `address-${tokenAddresses.size + 1}`;
      tokenAddresses.set(token, nextId);
      return send(200, { result: { code: 0 }, address: { id: nextId } });
    }
    if (request.url === '/api/lots/lot-1/deposit-holds/mock-pay') {
      if (!token || tokenAddresses.get(token) !== body.addressId) {
        return send(200, { result: { code: 40001, message: 'ADDRESS_REQUIRED' }, paid: false });
      }
      depositReadyTokens.add(token);
      return send(200, { result: { code: 0 }, paid: true, depositHold: { id: `hold-${token}` } });
    }
    if (request.url === '/api/lots/lot-1/bid') {
      if (!depositReadyTokens.has(token)) return send(200, { result: { code: 40002, message: 'DEPOSIT_REQUIRED' }, accepted: false });
      const key = request.headers['idempotency-key'] || '';
      return send(200, { result: { code: 0 }, accepted: true, bid: { id: `bid-${key}`, amount: body.amount } });
    }
    if (request.url === '/api/merchants/register' || request.url === '/api/users/register' || request.url === '/api/users/login') {
      return send(200, {
        result: { code: 0 },
        tokens: { accessToken: `token-${body.username}` },
        user: { id: `user-${body.username}` },
      });
    }
    return send(404, { error: request.url, lastProjectionReason });
  });
  await new Promise((resolveListen) => server.listen(0, '127.0.0.1', resolveListen));
  t.after(() => new Promise((resolveClose) => server.close(resolveClose)));
  const address = server.address();
  const baseURL = `http://127.0.0.1:${address.port}`;
  const script = resolve('scripts/assert-relay-recovery.mjs');
  const commonEnv = {
    ...process.env,
    PATH: `${binDir}:${process.env.PATH}`,
    BASE_URL: baseURL,
    AUCTION_SERVICE_OPERATIONS_URL: baseURL,
    RELAY_METRICS_URL: `${baseURL}/relay-metrics`,
    FAULT_SCENARIO: 'relay-kill',
    FAULT_SERVICE: 'outbox-relay',
    FAULT_RESULT_DIR: resultDir,
    FAULT_COMPOSE_FILE: composeFile,
    RELAY_ASSERTION_POLL_MS: '1',
    RELAY_ASSERTION_RECOVERY_TIMEOUT_MS: '1000',
    RELAY_ASSERTION_BID_COUNT: '4',
  };
  for (const currentPhase of ['before', 'during', 'after']) {
    phase = currentPhase;
    const result = await runCLI(process.execPath, [script, currentPhase, 'relay-kill', resultDir, 'outbox-relay', 'container-test'], {
      ...commonEnv, FAULT_PHASE: currentPhase,
    });
    assert.equal(result.code, 0, `${currentPhase} failed: ${result.stderr}`);
    assert.match(result.stdout, /"status":"PASS"/);
  }
  const archivedState = await readFile(join(resultDir, 'relay-assertion-state.json'), 'utf8');
  assert.doesNotMatch(archivedState, /accessToken|FaultAssert123|"token"/);
});

function projectionPayload(latest) {
  return {
    projection_gate: {
      ready: true,
      reason: 'healthy',
      total_lag_records: 0,
      max_partition_lag_records: 0,
      oldest_age_ms: 0,
      consecutive_healthy_polls: 3,
      checked_at_ms: Date.now(),
      partitions: [{
        partition: 0,
        earliest: 0,
        database_next_offset: latest,
        latest,
        lag_records: 0,
      }],
    },
  };
}

async function readRequestJSON(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {};
}

function runCLI(command, args, env) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, { env });
    const stdout = [];
    const stderr = [];
    child.stdout.on('data', (chunk) => stdout.push(chunk));
    child.stderr.on('data', (chunk) => stderr.push(chunk));
    child.once('error', rejectRun);
    child.once('close', (code) => resolveRun({
      code,
      stdout: Buffer.concat(stdout).toString('utf8'),
      stderr: Buffer.concat(stderr).toString('utf8'),
    }));
  });
}
