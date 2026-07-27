import { describe, expect, it } from 'vitest';
import type { Lot, RoomSnapshot } from '../api/types';
import type { RealtimeEnvelopeV1, RoomSnapshotAdminV1 } from './realtimeEnvelope';
import { RoomChannel } from './roomChannel';

const nowMs = 1_700_000_000_000;

function lot(version = 5): Lot {
  return {
    id: 'lot-channel', roomId: 'room-channel', title: '版本链拍品', description: '', imageUrl: '', status: 'LOT_STATUS_LIVE',
    rule: { startPrice: { amount: 10_000, currency: 'CNY' }, minIncrement: { amount: 100, currency: 'CNY' }, durationSeconds: 300, antiSnipeWindowSeconds: 10, antiSnipeExtendSeconds: 10, maxExtendCount: 3 },
    currentPrice: { amount: 12_000, currency: 'CNY' }, leadingUserId: 'buyer-1', leadingNickname: '买家一', startedAtUnixMs: nowMs - 10_000,
    endsAtUnixMs: nowMs + 290_000, settledAtUnixMs: 0, winnerUserId: '', winnerNickname: '', finalPrice: { amount: 0, currency: 'CNY' },
    version, trustCards: [], duelState: { active: false, lotId: '', userAId: '', userANickname: '', userBId: '', userBNickname: '', startedAtUnixMs: 0, endsAtUnixMs: 0, extendCount: 0, maxExtendCount: 0 }, playbookStage: 'PLAYBOOK_STAGE_WARM_UP', stats: { participantCount: 1, bidCount: 1 }, galleryImageUrls: [], tags: [],
  };
}

function snapshot(version = 5): RoomSnapshot {
  return { roomId: 'room-channel', currentLot: lot(version), ranking: [], recentBids: [], playbookStage: 'PLAYBOOK_STAGE_WARM_UP', serverTimeUnixMs: nowMs };
}

function adminSnapshot(version: number, patch: Partial<RoomSnapshotAdminV1> = {}): RoomSnapshotAdminV1 {
  return {
    mainAccountId: 'merchant-channel', roomId: 'room-channel', lotId: 'lot-channel', lotVersion: version,
    status: 'LOT_STATUS_LIVE', currentPriceFen: 12_000 + version * 100, endsAtUnixMs: nowMs + 290_000,
    topRanking: [{ rank: 1, userId: 'buyer-1', nickname: '买家一', avatarUrl: '', amountFen: 12_000 + version * 100, bidAtUnixMs: nowMs }],
    ...patch,
  };
}

function envelope(version: number, patch: Partial<RoomSnapshotAdminV1> = {}): RealtimeEnvelopeV1 {
  return { messageId: `message-${version}`, schemaVersion: 1, occurredAtUnixMs: nowMs + version, adminSnapshot: adminSnapshot(version, patch) };
}

describe('RoomChannel 运营版本链', () => {
  it('忽略乱序旧版本并保持最终状态不回退', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    expect(channel.ingest(envelope(4))).toMatchObject({ kind: 'ignored', reason: 'stale' });
    expect(channel.currentSnapshot()?.currentLot?.version).toBe(5);
  });

  it('相同版本幂等忽略', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    expect(channel.ingest(envelope(5))).toMatchObject({ kind: 'ignored', reason: 'duplicate' });
  });

  it('连续版本归并运营身份、价格与排名', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    const result = channel.ingest(envelope(6));
    expect(result).toMatchObject({ kind: 'applied', lotVersion: 6 });
    expect(channel.currentSnapshot()?.currentLot).toMatchObject({ version: 6, leadingUserId: 'buyer-1', leadingNickname: '买家一' });
  });

  it('跳版本不猜测中间状态而要求回源', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    expect(channel.ingest(envelope(8))).toEqual({ kind: 'recover', reason: 'version-gap', lotVersion: 8 });
    expect(channel.currentSnapshot()?.currentLot?.version).toBe(5);
  });

  it('切换拍品要求先恢复权威快照', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    expect(channel.ingest(envelope(1, { lotId: 'lot-next' }))).toEqual({ kind: 'recover', reason: 'lot-mismatch', lotVersion: 1 });
  });

  it('心跳一致时不回源，不一致时要求回源', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    const base = { messageId: 'heartbeat', schemaVersion: 1, occurredAtUnixMs: nowMs };
    expect(channel.ingest({ ...base, heartbeat: { lotId: 'lot-channel', authoritativeLotVersion: 5, status: 'LOT_STATUS_LIVE', serverTimeUnixMs: nowMs } })).toEqual({ kind: 'heartbeat', lotVersion: 5 });
    expect(channel.ingest({ ...base, heartbeat: { lotId: 'lot-channel', authoritativeLotVersion: 6, status: 'LOT_STATUS_LIVE', serverTimeUnixMs: nowMs } })).toEqual({ kind: 'recover', reason: 'heartbeat-mismatch', lotVersion: 6 });
  });

  it('终态连续版本成为最终状态且不会被旧消息覆盖', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    channel.ingest(envelope(6, { status: 'LOT_STATUS_SETTLED', currentPriceFen: 18_800 }));
    channel.ingest(envelope(5));
    expect(channel.currentSnapshot()?.currentLot).toMatchObject({ status: 'LOT_STATUS_SETTLED', version: 6, finalPrice: { amount: 18_800, currency: 'CNY' } });
  });

  it('忽略其他房间和后台不消费的私有 tombstone', () => {
    const channel = new RoomChannel('room-channel');
    channel.replaceSnapshot(snapshot());
    expect(channel.ingest(envelope(6, { roomId: 'room-other' }))).toMatchObject({ kind: 'ignored', reason: 'other-room' });
    expect(channel.ingest({ messageId: 'personal', schemaVersion: 1, occurredAtUnixMs: nowMs, personalDelta: { userId: 'buyer-1', lotId: 'lot-channel', lotVersion: 6, youAreLeading: false, orderVisibility: 'ORDER_VISIBILITY_NONE', tombstone: true } })).toEqual({ kind: 'ignored', reason: 'non-admin-payload', lotVersion: 0 });
    expect(channel.currentSnapshot()?.currentLot?.version).toBe(5);
  });
});
