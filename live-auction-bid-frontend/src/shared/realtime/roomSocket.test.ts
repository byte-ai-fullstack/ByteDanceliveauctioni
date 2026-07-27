// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { RoomSnapshot } from '../api/types';

vi.mock('./wsTicket', () => ({
  getWsTicket: vi.fn().mockResolvedValue('admin-ticket'),
}));

import { fullJitterDelay, RoomSocket } from './roomSocket';

describe('WebSocket full jitter 重连退避', () => {
  it('首轮分布在 0 到 500ms 之间', () => {
    expect(fullJitterDelay(1, () => 0)).toBe(0);
    expect(fullJitterDelay(1, () => 0.999)).toBe(499);
  });

  it('指数上限最终封顶 30 秒', () => {
    expect(fullJitterDelay(2, () => 0.5)).toBe(500);
    expect(fullJitterDelay(20, () => 0.99999)).toBe(29_999);
  });

  it('同一轮随机样本覆盖退避窗口而非固定抖动', () => {
    const samples = [0.02, 0.2, 0.5, 0.8, 0.98].map((value) => fullJitterDelay(4, () => value));
    expect(samples).toEqual([80, 800, 2_000, 3_200, 3_920]);
    expect(new Set(samples).size).toBe(samples.length);
  });
});

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];

  readonly url: string;
  onopen: (() => void) | null = null;
  onmessage: ((message: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  open() {
    this.onopen?.();
  }

  message(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }

  close() {
    this.onclose?.();
  }
}

function snapshot(version = 5): RoomSnapshot {
  return {
    roomId: 'room-admin',
    currentLot: {
      id: 'lot-admin', roomId: 'room-admin', title: '运营拍品', description: '', imageUrl: '', status: 'LOT_STATUS_LIVE',
      rule: { startPrice: { amount: 10_000, currency: 'CNY' }, minIncrement: { amount: 100, currency: 'CNY' }, durationSeconds: 60, antiSnipeWindowSeconds: 10, antiSnipeExtendSeconds: 30, maxExtendCount: 3 },
      currentPrice: { amount: 10_000, currency: 'CNY' }, leadingUserId: '', leadingNickname: '', startedAtUnixMs: 1_000,
      endsAtUnixMs: 61_000, settledAtUnixMs: 0, winnerUserId: '', winnerNickname: '', finalPrice: { amount: 0, currency: 'CNY' },
      version, trustCards: [], duelState: { active: false, lotId: '', userAId: '', userANickname: '', userBId: '', userBNickname: '', startedAtUnixMs: 0, endsAtUnixMs: 0, extendCount: 0, maxExtendCount: 0 },
      playbookStage: 'PLAYBOOK_STAGE_WARM_UP', stats: { participantCount: 0, bidCount: 0 },
    },
    ranking: [], recentBids: [], playbookStage: 'PLAYBOOK_STAGE_WARM_UP', serverTimeUnixMs: 2_000,
  };
}

function adminEnvelope(version: number) {
  return {
    messageId: `admin-${version}`,
    schemaVersion: 1,
    occurredAtUnixMs: 2_000 + version,
    adminSnapshot: {
      mainAccountId: 'merchant-admin', roomId: 'room-admin', lotId: 'lot-admin', lotVersion: version,
      status: 'LOT_STATUS_LIVE', currentPriceFen: 10_000 + version, endsAtUnixMs: 61_000, topRanking: [],
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function flushMessages() {
  for (let index = 0; index < 12; index += 1) await Promise.resolve();
}

describe('运营 WebSocket 恢复边界', () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeWebSocket);
    vi.useFakeTimers();
    vi.spyOn(Math, 'random').mockReturnValue(0);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('心跳超时后关闭失效连接并用 full jitter 重连', async () => {
    const statuses: string[] = [];
    const socket = new RoomSocket({
      roomId: 'room-admin', heartbeatTimeoutMs: 1_000,
      recoverSnapshot: async () => snapshot(),
      onStatusChange: (status) => statuses.push(status),
    });

    socket.connect();
    await flushMessages();
    FakeWebSocket.instances[0]?.open();
    await flushMessages();
    expect(FakeWebSocket.instances).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(1_000);
    await vi.advanceTimersByTimeAsync(1);
    await flushMessages();
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(statuses).toContain('reconnecting');
    socket.close();
  });

  it('旧连接的在途快照和排队消息不能覆盖新连接', async () => {
    const oldRecovery = deferred<RoomSnapshot>();
    const recoverSnapshot = vi.fn()
      .mockReturnValueOnce(oldRecovery.promise)
      .mockResolvedValueOnce(snapshot(7));
    const events: number[] = [];
    const recovered: number[] = [];
    const socket = new RoomSocket({
      roomId: 'room-admin', recoverSnapshot,
      onEvent: (_event, meta) => events.push(meta.lotVersion),
      onSnapshot: (_snapshot, meta) => recovered.push(meta.lotVersion),
    });

    socket.connect();
    await flushMessages();
    const oldConnection = FakeWebSocket.instances[0]!;
    oldConnection.open();
    oldConnection.message(adminEnvelope(6));
    oldConnection.close();
    await vi.advanceTimersByTimeAsync(0);
    await flushMessages();
    const newConnection = FakeWebSocket.instances[1]!;
    newConnection.open();
    await flushMessages();

    oldRecovery.resolve(snapshot(5));
    await flushMessages();
    expect(recoverSnapshot).toHaveBeenCalledTimes(2);
    expect(recovered).toEqual([7]);
    expect(events).toEqual([]);
    socket.close();
  });
});
