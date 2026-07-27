import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

import {
  evaluateAfterDatabase,
  normalizeProjectionSnapshot,
  overloadedResult,
  partitionLatestSum,
  sqlQuote,
} from './assert-projector-recovery.mjs';

test('normalizes the operations projection payload and sums partition watermarks', () => {
  const snapshot = normalizeProjectionSnapshot({
    projection_gate: {
      ready: false,
      reason: 'oldest_age_limit',
      total_lag_records: 2,
      max_partition_lag_records: 2,
      oldest_age_ms: 31_000,
      consecutive_healthy_polls: 0,
      checked_at_ms: 100,
      partitions: [
        { partition: 0, earliest: 0, database_next_offset: 5, latest: 7, lag_records: 2 },
        { partition: 1, earliest: 0, database_next_offset: 9, latest: 9, lag_records: 0 },
      ],
    },
  });
  assert.equal(snapshot.reason, 'oldest_age_limit');
  assert.equal(snapshot.totalLagRecords, 2);
  assert.equal(partitionLatestSum(snapshot), 16);
});

test('rejects structurally incomplete projection evidence', () => {
  assert.throws(
    () => normalizeProjectionSnapshot({ projection_gate: { ready: true, reason: 'healthy', total_lag_records: 0 } }),
    /invalid projection snapshot|structurally incomplete/,
  );
});

test('recognizes the stable overloaded contract', () => {
  assert.equal(overloadedResult({ result: { code: 503001, message: 'OVERLOADED' } }), true);
  assert.equal(overloadedResult({ result: { code: 0, message: 'OK' } }), false);
});

test('post-recovery database evidence enforces uniqueness and version continuity', () => {
  const state = { acceptedAmount: 10_200, baselineDomainPending: 1 };
  const passing = {
    acceptedBidCount: 1,
    rejectedBidCount: 0,
    lotStatus: 4,
    currentPrice: 10_200,
    lotVersion: 5,
    projectionVersion: 5,
    maxInboxVersion: 5,
    newHighFindings: 0,
    domainPending: 0,
  };
  assert.deepEqual(evaluateAfterDatabase(passing, state), []);
  const failures = evaluateAfterDatabase({
    ...passing,
    acceptedBidCount: 2,
    rejectedBidCount: 1,
    projectionVersion: 4,
    newHighFindings: 1,
    domainPending: 2,
  }, state);
  assert.equal(failures.length, 5);
});

test('quotes MySQL string literals without allowing quote or slash injection', () => {
  assert.equal(sqlQuote("lot\\path' OR 1=1"), "'lot\\\\path'' OR 1=1'");
});

test('CLI proves a complete before/during/after projector recovery lifecycle', async (t) => {
  const scratch = await mkdtemp(join(tmpdir(), 'projector-assertion-'));
  t.after(() => rm(scratch, { recursive: true, force: true }));
  const resultDir = join(scratch, 'results');
  const binDir = join(scratch, 'bin');
  await mkdir(resultDir, { recursive: true });
  await mkdir(binDir, { recursive: true });
  const composeFile = join(scratch, 'compose.yml');
  await writeFile(composeFile, 'services: {}\n');
  const fakeDocker = join(binDir, 'docker');
  await writeFile(fakeDocker, `#!/usr/bin/env bash
set -euo pipefail
/usr/bin/cat >/dev/null
if [[ "\${FAULT_PHASE:-}" == "before" ]]; then
  printf '%s\\n' '{"databaseNowMs":1000,"acceptedBidCount":1,"rejectedBidCount":0,"lotStatus":2,"lotVersion":3,"currentPrice":10100,"projectionVersion":3,"maxInboxVersion":3,"domainPending":0,"unresolvedHighFindings":0,"newHighFindings":0}'
else
  printf '%s\\n' '{"databaseNowMs":2000,"acceptedBidCount":1,"rejectedBidCount":0,"lotStatus":4,"lotVersion":5,"currentPrice":10200,"projectionVersion":5,"maxInboxVersion":5,"domainPending":0,"unresolvedHighFindings":0,"newHighFindings":0}'
fi
`, { mode: 0o755 });

  let phase = 'before';
  let cancelled = false;
  let afterProjectionCalls = 0;
  let lastProjectionReason = 'healthy';
  const tokenAddresses = new Map();
  const depositReadyTokens = new Set();
  const server = http.createServer(async (request, response) => {
    const body = await readRequestJSON(request);
    const auth = request.headers.authorization || '';
    const token = auth.startsWith('Bearer ') ? auth.slice('Bearer '.length) : '';
    const send = (status, payload) => {
      response.writeHead(status, { 'Content-Type': 'application/json' });
      response.end(`${JSON.stringify(payload)}\n`);
    };
    if (request.url === '/readyz') return send(200, { ok: true });
    if (request.url === '/admissionz') {
      const open = phase === 'before' || (phase === 'after' && lastProjectionReason === 'healthy');
      return send(open ? 200 : 503, { status: open ? 'open' : 'closed' });
    }
    if (request.url === '/metrics/runtime-projection') {
      let projection;
      if (phase === 'before') projection = projectionPayload('healthy', true, 0, 5, 5, 3);
      if (phase === 'during') projection = projectionPayload('oldest_age_limit', false, cancelled ? 6 : 5, 5, cancelled ? 11 : 10, 0);
      if (phase === 'after') {
        afterProjectionCalls += 1;
        projection = afterProjectionCalls === 1
          ? projectionPayload('recovering', false, 0, 11, 11, 1)
          : projectionPayload('healthy', true, 0, 11, 11, 3);
      }
      lastProjectionReason = projection.projection_gate.reason;
      return send(200, projection);
    }
    if (request.url === '/api/admin/rooms') return send(200, { result: { code: 0 }, rooms: [{ id: 'room-1' }] });
    if (request.url === '/api/lots' && request.method === 'POST') {
      return send(200, { result: { code: 0 }, lot: { id: 'lot-1', room_id: body.room_id } });
    }
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
      if (key.includes('during-rejected')) return send(200, { result: { code: 503001, message: 'OVERLOADED' }, accepted: false });
      return send(200, { result: { code: 0 }, accepted: true, bid: { id: `bid-${key}`, amount: body.amount } });
    }
    if (request.url === '/api/lots/lot-1/cancel') {
      cancelled = true;
      return send(200, { result: { code: 0 }, lot: { id: 'lot-1', status: 'LOT_STATUS_CANCELLED' }, event: { id: 'event-cancel' } });
    }
    if (request.url === '/api/merchants/register' || request.url === '/api/users/register') {
      return send(200, {
        result: { code: 0 },
        tokens: { accessToken: `token-${body.username}` },
        user: { id: `user-${body.username}` },
      });
    }
    if (request.url === '/api/users/login') {
      return send(200, {
        result: { code: 0 },
        tokens: { accessToken: `token-${body.username}` },
        user: { id: `user-${body.username}` },
      });
    }
    return send(404, { error: request.url });
  });
  await new Promise((resolveListen) => server.listen(0, '127.0.0.1', resolveListen));
  t.after(() => new Promise((resolveClose) => server.close(resolveClose)));
  const address = server.address();
  const baseURL = `http://127.0.0.1:${address.port}`;
  const script = resolve('scripts/assert-projector-recovery.mjs');
  const commonEnv = {
    ...process.env,
    PATH: `${binDir}:${process.env.PATH}`,
    BASE_URL: baseURL,
    AUCTION_SERVICE_OPERATIONS_URL: baseURL,
    FAULT_SCENARIO: 'projector-pause',
    FAULT_SERVICE: 'projector',
    FAULT_RESULT_DIR: resultDir,
    FAULT_COMPOSE_FILE: composeFile,
    PROJECTOR_ASSERTION_POLL_MS: '1',
    PROJECTOR_ASSERTION_CLOSE_TIMEOUT_MS: '1000',
    PROJECTOR_ASSERTION_RECOVERY_TIMEOUT_MS: '1000',
    PROJECTOR_ASSERTION_REJECT_OBSERVE_MS: '1000',
  };
  for (const currentPhase of ['before', 'during', 'after']) {
    phase = currentPhase;
    const result = await runCLI(process.execPath, [script, currentPhase, 'projector-pause', resultDir, 'projector', 'container-test'], {
      ...commonEnv, FAULT_PHASE: currentPhase,
    });
    assert.equal(result.code, 0, `${currentPhase} failed: ${result.stderr}`);
    assert.match(result.stdout, /"status":"PASS"/);
  }
  const archivedState = await readFile(join(resultDir, 'projector-assertion-state.json'), 'utf8');
  assert.doesNotMatch(archivedState, /accessToken|FaultAssert123|"token"/);
});

function projectionPayload(reason, ready, lag, databaseNext, latest, healthyPolls) {
  return {
    projection_gate: {
      ready,
      reason,
      total_lag_records: lag,
      max_partition_lag_records: lag,
      oldest_age_ms: lag ? 31_000 : 0,
      consecutive_healthy_polls: healthyPolls,
      checked_at_ms: Date.now(),
      partitions: [{
        partition: 0,
        earliest: 0,
        database_next_offset: databaseNext,
        latest,
        lag_records: lag,
      }],
    },
  };
}

async function readRequestJSON(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  if (!chunks.length) return {};
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
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
