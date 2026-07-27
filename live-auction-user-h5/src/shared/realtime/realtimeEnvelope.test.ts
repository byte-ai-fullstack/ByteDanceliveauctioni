import { describe, expect, it } from 'vitest';
import { LOT_STATUS, type RoomSnapshot } from '../api/types';
import {
  heartbeatRequiresRecovery,
  mergePublicRealtimeState,
  normalizeRealtimeEnvelope,
  type PersonalDeltaV1,
  type RoomSnapshotPublicV1,
} from './realtimeEnvelope';

function snapshot(version = 7): RoomSnapshot {
  return {
    roomId: 'room-1',
    serverTimeUnixMs: 1_000,
    currentLot: {
      id: 'lot-1',
      roomId: 'room-1',
      title: '测试拍品',
      status: LOT_STATUS.LIVE,
      currentPrice: { amount: 12_000, currency: 'CNY' },
      leadingUserId: 'buyer-a',
      leadingNickname: '买家甲',
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
      { rank: 1, userId: 'buyer-a', nickname: '买家甲', avatarUrl: 'a.png', amount: { amount: 12_000, currency: 'CNY' } },
      { rank: 2, userId: 'buyer-b', nickname: '买家乙', avatarUrl: 'b.png', amount: { amount: 11_500, currency: 'CNY' } },
    ],
    recentBids: [],
  };
}

function publicState(version = 7): RoomSnapshotPublicV1 {
  return {
    roomId: 'room-1',
    lotId: 'lot-1',
    lotVersion: version,
    status: LOT_STATUS.LIVE,
    currentPriceFen: 12_000,
    endsAtUnixMs: 9_000,
    bidCount: 3,
    topRanking: [
      { rank: 1, maskedNickname: '买***甲', maskedAvatarUrl: 'masked-a.png', amountFen: 12_000, bidAtUnixMs: 800 },
      { rank: 2, maskedNickname: '买***乙', maskedAvatarUrl: 'masked-b.png', amountFen: 11_500, bidAtUnixMs: 700 },
    ],
  };
}

function personal(overrides: Partial<PersonalDeltaV1> = {}): PersonalDeltaV1 {
  return {
    userId: 'buyer-a',
    lotId: 'lot-1',
    lotVersion: 7,
    yourRank: 1,
    yourAmountFen: 12_000,
    youAreLeading: true,
    orderVisibility: 'ORDER_VISIBILITY_NONE',
    tombstone: false,
    ...overrides,
  };
}

describe('realtime envelope V1', () => {
  it('normalizes protobuf JSON int64 strings and settlement fields', () => {
    const envelope = normalizeRealtimeEnvelope({
      messageId: 'message-1',
      schemaVersion: 1,
      occurredAtUnixMs: '1700000000000',
      publicSnapshot: {
        roomId: 'room-1',
        lotId: 'lot-1',
        lotVersion: '8',
        status: LOT_STATUS.SETTLED,
        currentPriceFen: '13500',
        endsAtUnixMs: '1700000000100',
        bidCount: '4',
        topRanking: [{ rank: 1, maskedNickname: '买***甲', maskedAvatarUrl: '', amountFen: '13500', bidAtUnixMs: '1699999999000' }],
        settlement: {
          status: LOT_STATUS.SETTLED,
          finalPriceFen: '13500',
          maskedWinnerNickname: '买***甲',
          settledAtUnixMs: '1700000000100',
          cancelReason: '',
        },
      },
    });

    expect(envelope.occurredAtUnixMs).toBe(1_700_000_000_000);
    expect(envelope.publicSnapshot?.lotVersion).toBe(8);
    expect(envelope.publicSnapshot?.settlement?.finalPriceFen).toBe(13_500);
  });

  it('rejects unsupported schemas and envelopes with multiple payloads', () => {
    expect(() => normalizeRealtimeEnvelope({ schemaVersion: 2, heartbeat: {} })).toThrow('unsupported realtime schema');
    expect(() => normalizeRealtimeEnvelope({
      schemaVersion: 1,
      heartbeat: { lotId: '', authoritativeLotVersion: 0, status: LOT_STATUS.UNSPECIFIED, serverTimeUnixMs: 0 },
      reconnect: { retryAfterMs: 100, reason: 'drain' },
    })).toThrow('exactly one payload');
  });

  it('keeps public ranking anonymous and overlays only the authenticated buyer', () => {
    const publicOnly = mergePublicRealtimeState(snapshot(), publicState(), undefined, 2_000);
    expect(publicOnly?.ranking).toEqual([
      expect.objectContaining({ rank: 1, userId: '', nickname: '买***甲', avatarUrl: 'masked-a.png' }),
      expect.objectContaining({ rank: 2, userId: '', nickname: '买***乙', avatarUrl: 'masked-b.png' }),
    ]);

    const withOwnDelta = mergePublicRealtimeState(snapshot(), publicState(), personal(), 2_000);
    expect(withOwnDelta?.ranking?.[0]).toEqual(expect.objectContaining({ userId: 'buyer-a', nickname: '买家甲', avatarUrl: 'a.png' }));
    expect(withOwnDelta?.ranking?.[1]).toEqual(expect.objectContaining({ userId: '', nickname: '买***乙', avatarUrl: 'masked-b.png' }));
    expect(JSON.stringify(withOwnDelta)).not.toContain('buyer-b');
    expect(JSON.stringify(withOwnDelta)).not.toContain('买家乙');
  });

  it('drops stale personal state on version advance and applies tombstones', () => {
    const advanced = mergePublicRealtimeState(snapshot(7), publicState(8), personal({ lotVersion: 7 }), 3_000);
    expect(advanced?.currentLot?.version).toBe(8);
    expect(advanced?.currentLot?.leadingUserId).toBe('');
    expect(advanced?.ranking?.every((item) => item.userId === '')).toBe(true);

    const tombstoned = mergePublicRealtimeState(snapshot(8), publicState(8), personal({ lotVersion: 8, tombstone: true }), 3_100);
    expect(tombstoned?.currentLot?.leadingUserId).toBe('');
    expect(tombstoned?.ranking?.every((item) => item.userId === '')).toBe(true);
  });

  it('accepts version jumps and exposes a winner only from explicit private order visibility', () => {
    const jumped = mergePublicRealtimeState(snapshot(7), {
      ...publicState(10),
      status: LOT_STATUS.SETTLED,
      settlement: {
        status: LOT_STATUS.SETTLED,
        finalPriceFen: 14_000,
        maskedWinnerNickname: '买***甲',
        settledAtUnixMs: 4_000,
        cancelReason: '',
      },
    }, personal({ lotVersion: 10, orderVisibility: 'ORDER_VISIBILITY_READY' }), 4_000);

    expect(jumped?.currentLot?.version).toBe(10);
    expect(jumped?.currentLot?.winnerUserId).toBe('buyer-a');
    expect(jumped?.currentLot?.finalPrice).toEqual({ amount: 14_000, currency: 'CNY' });
  });

  it('requires HTTP recovery when heartbeat identity, version, or status diverges', () => {
    const current = snapshot(7);
    expect(heartbeatRequiresRecovery(current, {
      lotId: 'lot-1', authoritativeLotVersion: 7, status: LOT_STATUS.LIVE, serverTimeUnixMs: 2_000,
    })).toBe(false);
    expect(heartbeatRequiresRecovery(current, {
      lotId: 'lot-1', authoritativeLotVersion: 8, status: LOT_STATUS.LIVE, serverTimeUnixMs: 2_000,
    })).toBe(true);
    expect(heartbeatRequiresRecovery(current, {
      lotId: 'lot-2', authoritativeLotVersion: 1, status: LOT_STATUS.LIVE, serverTimeUnixMs: 2_000,
    })).toBe(true);
  });
});
