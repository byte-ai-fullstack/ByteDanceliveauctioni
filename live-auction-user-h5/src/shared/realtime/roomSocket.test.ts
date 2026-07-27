import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LOT_STATUS, type AuctionSocketEvent, type RoomSnapshot } from '../api/types';

vi.mock('./wsTicket', () => ({
  getPublicWsTicket: vi.fn().mockResolvedValue('ticket-1'),
}));

import { RoomSocket } from './roomSocket';
import type { RoomPersonalRecovery } from './personalRecovery';

class FakeWebSocket {
  static readonly OPEN = 1;
  static instances: FakeWebSocket[] = [];

  readonly url: string;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((message: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  message(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }

  close() {
    this.readyState = 3;
    this.onclose?.();
  }
}

function snapshot(version = 7): RoomSnapshot {
  return {
    roomId: 'room-1',
    currentLot: {
      id: 'lot-1',
      roomId: 'room-1',
      title: '测试拍品',
      status: LOT_STATUS.LIVE,
      currentPrice: { amount: 12_000, currency: 'CNY' },
      stats: { participantCount: 2, bidCount: 3 },
      rule: {
        startPrice: { amount: 10_000, currency: 'CNY' },
        minIncrement: { amount: 500, currency: 'CNY' },
        durationSeconds: 60,
        antiSnipeWindowSeconds: 10,
        antiSnipeExtendSeconds: 15,
      },
      version,
    },
    ranking: [
      { rank: 1, userId: 'buyer-a', nickname: '买家甲', amount: { amount: 12_000, currency: 'CNY' } },
      { rank: 2, userId: 'buyer-b', nickname: '买家乙', amount: { amount: 11_500, currency: 'CNY' } },
    ],
  };
}

function envelope(payload: Record<string, unknown>, messageId: string) {
  return { messageId, schemaVersion: 1, occurredAtUnixMs: 2_000, ...payload };
}

function publicSnapshot(version: number) {
  return {
    roomId: 'room-1',
    lotId: 'lot-1',
    lotVersion: version,
    status: LOT_STATUS.LIVE,
    currentPriceFen: 12_000 + version,
    endsAtUnixMs: 9_000,
    bidCount: version,
    topRanking: [
      { rank: 1, maskedNickname: '买***甲', maskedAvatarUrl: '', amountFen: 12_000 + version, bidAtUnixMs: 1_900 },
      { rank: 2, maskedNickname: '买***乙', maskedAvatarUrl: '', amountFen: 11_500, bidAtUnixMs: 1_800 },
    ],
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

describe('RoomSocket realtime V1 ordering', () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeWebSocket);
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('buffers a personal delta until the matching public version arrives', async () => {
    const events: AuctionSocketEvent[] = [];
    const socket = new RoomSocket({
      roomId: 'room-1',
      onEvent: (event) => events.push(event),
      onSnapshotRecovery: async () => snapshot(7),
    });

    socket.connect();
    await flushMessages();
    const connection = FakeWebSocket.instances[0]!;
    connection.open();
    await flushMessages();

    connection.message(envelope({
      personalDelta: {
        userId: 'buyer-a', lotId: 'lot-1', lotVersion: 8, yourRank: 1, yourAmountFen: 12_008,
        youAreLeading: true, orderVisibility: 'ORDER_VISIBILITY_NONE', tombstone: false,
      },
    }, 'personal-8'));
    connection.message(envelope({ publicSnapshot: publicSnapshot(8) }, 'public-8'));
    await flushMessages();

    expect(events).toHaveLength(1);
    expect(events[0]?.lot).toEqual(expect.objectContaining({ version: 8, leadingUserId: 'buyer-a' }));
    expect(events[0]?.ranking?.[0]?.userId).toBe('buyer-a');
    expect(events[0]?.ranking?.[1]?.userId).toBe('');
    socket.disconnect();
  });

  it('ignores a public event older than the recovered HTTP snapshot', async () => {
    const events: AuctionSocketEvent[] = [];
    const socket = new RoomSocket({
      roomId: 'room-1',
      onEvent: (event) => events.push(event),
      onSnapshotRecovery: async () => snapshot(9),
    });

    socket.connect();
    await flushMessages();
    const connection = FakeWebSocket.instances[0]!;
    connection.open();
    connection.message(envelope({ publicSnapshot: publicSnapshot(8) }, 'public-8'));
    await flushMessages();

    expect(events).toHaveLength(0);
    socket.disconnect();
  });

  it('does not apply queued messages from a superseded connection', async () => {
    const events: AuctionSocketEvent[] = [];
    const socket = new RoomSocket({
      roomId: 'room-1',
      onEvent: (event) => events.push(event),
      onSnapshotRecovery: async () => snapshot(7),
    });

    socket.connect();
    await flushMessages();
    const connection = FakeWebSocket.instances[0]!;
    connection.open();
    await flushMessages();
    connection.message(envelope({ publicSnapshot: publicSnapshot(8) }, 'public-8'));
    socket.reconnect();
    await flushMessages();

    expect(events).toHaveLength(0);
    socket.disconnect();
  });

  it('coalesces a missing private delta and recovers only the current user', async () => {
    const events: AuctionSocketEvent[] = [];
    const recoverPersonal = vi.fn().mockResolvedValue({
      personalDelta: {
        userId: 'buyer-a', lotId: 'lot-1', lotVersion: 8, yourRank: 1, yourAmountFen: 12_008,
        youAreLeading: true, orderVisibility: 'ORDER_VISIBILITY_NONE' as const, tombstone: false,
      },
      retryAfterMs: 0,
    });
    const socket = new RoomSocket({
      roomId: 'room-1',
      onEvent: (event) => events.push(event),
      onSnapshotRecovery: async () => snapshot(7),
      onPersonalRecovery: recoverPersonal,
    });

    socket.connect();
    await flushMessages();
    const connection = FakeWebSocket.instances[0]!;
    connection.open();
    await flushMessages();
    connection.message(envelope({ publicSnapshot: publicSnapshot(8) }, 'public-8'));
    await flushMessages();

    expect(recoverPersonal).not.toHaveBeenCalled();
    expect(events.at(-1)?.lot?.leadingUserId).toBe('');
    await vi.advanceTimersByTimeAsync(100);
    await flushMessages();

    expect(recoverPersonal).toHaveBeenCalledTimes(1);
    expect(events.at(-1)?.lot?.leadingUserId).toBe('buyer-a');
    expect(events.at(-1)?.ranking?.[1]?.userId).toBe('');
    socket.disconnect();
  });

  it('polls CREATING personal state until the projected order is READY', async () => {
    const recoverPersonal = vi.fn()
      .mockResolvedValueOnce({
        personalDelta: {
          userId: 'buyer-a', lotId: 'lot-1', lotVersion: 8,
          youAreLeading: true, yourOrderId: 'order-1', orderVisibility: 'ORDER_VISIBILITY_CREATING' as const, tombstone: false,
        },
        retryAfterMs: 250,
      })
      .mockResolvedValueOnce({
        personalDelta: {
          userId: 'buyer-a', lotId: 'lot-1', lotVersion: 8,
          youAreLeading: true, yourOrderId: 'order-1', orderVisibility: 'ORDER_VISIBILITY_READY' as const, tombstone: false,
        },
        retryAfterMs: 0,
      });
    const socket = new RoomSocket({
      roomId: 'room-1',
      onEvent: () => undefined,
      onSnapshotRecovery: async () => snapshot(7),
      onPersonalRecovery: recoverPersonal,
    });

    socket.connect();
    await flushMessages();
    const connection = FakeWebSocket.instances[0]!;
    connection.open();
    await flushMessages();
    connection.message(envelope({ publicSnapshot: publicSnapshot(8) }, 'public-8'));
    await flushMessages();
    await vi.advanceTimersByTimeAsync(100);
    await flushMessages();
    expect(recoverPersonal).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(250);
    await flushMessages();
    expect(recoverPersonal).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(5_000);
    await flushMessages();
    expect(recoverPersonal).toHaveBeenCalledTimes(2);
    socket.disconnect();
  });

  it('does not let an older HTTP recovery overwrite a newer private websocket delta', async () => {
    const events: AuctionSocketEvent[] = [];
    const pendingRecovery = deferred<RoomPersonalRecovery>();
    const recoverPersonal = vi.fn().mockReturnValue(pendingRecovery.promise);
    const socket = new RoomSocket({
      roomId: 'room-1',
      onEvent: (event) => events.push(event),
      onSnapshotRecovery: async () => snapshot(7),
      onPersonalRecovery: recoverPersonal,
    });

    socket.connect();
    await flushMessages();
    const connection = FakeWebSocket.instances[0]!;
    connection.open();
    await flushMessages();
    connection.message(envelope({ publicSnapshot: publicSnapshot(8) }, 'public-8'));
    await vi.advanceTimersByTimeAsync(100);
    await flushMessages();
    expect(recoverPersonal).toHaveBeenCalledTimes(1);

    connection.message(envelope({
      personalDelta: {
        userId: 'buyer-a', lotId: 'lot-1', lotVersion: 8,
        youAreLeading: true, yourOrderId: 'order-1', orderVisibility: 'ORDER_VISIBILITY_READY', tombstone: false,
      },
    }, 'personal-ready-8'));
    await flushMessages();
    expect(events).toHaveLength(2);

    pendingRecovery.resolve({
      personalDelta: {
        userId: 'buyer-a', lotId: 'lot-1', lotVersion: 8,
        youAreLeading: true, yourOrderId: 'order-1', orderVisibility: 'ORDER_VISIBILITY_CREATING', tombstone: false,
      },
      retryAfterMs: 250,
    });
    await flushMessages();
    await vi.advanceTimersByTimeAsync(5_000);
    await flushMessages();

    expect(events).toHaveLength(2);
    expect(recoverPersonal).toHaveBeenCalledTimes(1);
    socket.disconnect();
  });

  it('starts a generation-scoped snapshot recovery after reconnect and discards the old result', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0);
    const oldRecovery = deferred<RoomSnapshot>();
    const recoverSnapshot = vi.fn()
      .mockReturnValueOnce(oldRecovery.promise)
      .mockResolvedValueOnce(snapshot(9));
    const events: AuctionSocketEvent[] = [];
    const socket = new RoomSocket({
      roomId: 'room-1',
      onEvent: (event) => events.push(event),
      onSnapshotRecovery: recoverSnapshot,
    });

    socket.connect();
    await flushMessages();
    FakeWebSocket.instances[0]!.open();
    await flushMessages();
    socket.reconnect();
    await vi.advanceTimersByTimeAsync(0);
    await flushMessages();
    const currentConnection = FakeWebSocket.instances[1]!;
    currentConnection.open();
    await flushMessages();

    oldRecovery.resolve(snapshot(7));
    await flushMessages();
    expect(recoverSnapshot).toHaveBeenCalledTimes(2);

    currentConnection.message(envelope({ publicSnapshot: publicSnapshot(8) }, 'stale-public-8'));
    await flushMessages();
    expect(events).toEqual([]);
    socket.disconnect();
  });
});
