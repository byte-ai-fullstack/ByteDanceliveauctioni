import type { AuctionEvent, EventType, Money, RankingItem, RoomSnapshot } from '../api/types';
import type { LotStatus, PersonalDeltaV1, RealtimeEnvelopeV1, RoomHeartbeatV1, RoomSnapshotAdminV1, RoomSnapshotPublicV1 } from './generated/realtime.contract';

export type { RealtimeEnvelopeV1, RoomHeartbeatV1, RoomSnapshotAdminV1 } from './generated/realtime.contract';

const REALTIME_SCHEMA_VERSION = 1;

const LOT_STATUSES = new Set<LotStatus>([
  'LOT_STATUS_UNSPECIFIED', 'LOT_STATUS_DRAFT', 'LOT_STATUS_READY', 'LOT_STATUS_QUEUED',
  'LOT_STATUS_LIVE', 'LOT_STATUS_EXTENDED', 'LOT_STATUS_SETTLED', 'LOT_STATUS_CANCELLED', 'LOT_STATUS_FAILED',
]);

export function normalizeRealtimeEnvelope(input: unknown): RealtimeEnvelopeV1 {
  const raw = record(input, 'realtime envelope');
  const schemaVersion = integer(raw.schemaVersion);
  if (schemaVersion !== REALTIME_SCHEMA_VERSION) throw new Error(`不支持的实时协议版本：${schemaVersion}`);
  const envelope: RealtimeEnvelopeV1 = {
    messageId: text(raw.messageId),
    schemaVersion,
    occurredAtUnixMs: integer(raw.occurredAtUnixMs),
  };
  if (raw.publicSnapshot) envelope.publicSnapshot = normalizePublicSnapshot(raw.publicSnapshot);
  if (raw.personalDelta) envelope.personalDelta = normalizePersonalDelta(raw.personalDelta);
  if (raw.heartbeat) envelope.heartbeat = normalizeHeartbeat(raw.heartbeat);
  if (raw.adminSnapshot) envelope.adminSnapshot = normalizeAdminSnapshot(raw.adminSnapshot);
  if (raw.reconnect) {
    const reconnect = record(raw.reconnect, 'reconnect');
    envelope.reconnect = { retryAfterMs: integer(reconnect.retryAfterMs), reason: text(reconnect.reason) };
  }
  const payloadCount = [envelope.publicSnapshot, envelope.personalDelta, envelope.heartbeat, envelope.adminSnapshot, envelope.reconnect].filter(Boolean).length;
  if (payloadCount !== 1) throw new Error('实时消息必须且只能包含一个 payload');
  return envelope;
}

export function mergeAdminSnapshot(current: RoomSnapshot | null, incoming: RoomSnapshotAdminV1, serverTimeUnixMs: number): RoomSnapshot | null {
  const currentLot = current?.currentLot;
  if (!current || !currentLot || currentLot.id !== incoming.lotId) return null;
  const currentVersion = integer(currentLot.version);
  if (incoming.lotVersion < currentVersion) return current;
  const ranking: RankingItem[] = incoming.topRanking.map((item) => ({
    rank: item.rank,
    userId: item.userId,
    nickname: item.nickname,
    amount: money(item.amountFen),
    bidAtUnixMs: item.bidAtUnixMs,
  }));
  const leader = ranking[0];
  const terminal = isTerminal(incoming.status);
  return {
    ...current,
    serverTimeUnixMs,
    ranking,
    currentLot: {
      ...currentLot,
      status: incoming.status,
      version: incoming.lotVersion,
      currentPrice: money(incoming.currentPriceFen),
      endsAtUnixMs: incoming.endsAtUnixMs,
      leadingUserId: terminal ? '' : leader?.userId ?? '',
      leadingNickname: terminal ? '' : leader?.nickname ?? '',
      winnerUserId: incoming.status === 'LOT_STATUS_SETTLED' ? leader?.userId ?? currentLot.winnerUserId : currentLot.winnerUserId,
      winnerNickname: incoming.status === 'LOT_STATUS_SETTLED' ? leader?.nickname ?? currentLot.winnerNickname : currentLot.winnerNickname,
      finalPrice: incoming.status === 'LOT_STATUS_SETTLED' ? money(incoming.currentPriceFen) : currentLot.finalPrice,
    },
  };
}

export function adminSnapshotEvent(envelope: RealtimeEnvelopeV1, snapshot: RoomSnapshot): AuctionEvent {
  const admin = envelope.adminSnapshot;
  if (!admin || !snapshot.currentLot) throw new Error('运营实时快照不完整');
  return {
    id: envelope.messageId,
    type: eventTypeForStatus(admin.status),
    roomId: admin.roomId,
    lotId: admin.lotId,
    occurredAtUnixMs: envelope.occurredAtUnixMs,
    lot: snapshot.currentLot,
    ranking: snapshot.ranking,
  };
}

export function heartbeatRequiresRecovery(snapshot: RoomSnapshot | null, heartbeat: RoomHeartbeatV1): boolean {
  const lot = snapshot?.currentLot;
  if (!lot) return heartbeat.lotId !== '' || heartbeat.authoritativeLotVersion !== 0;
  return lot.id !== heartbeat.lotId
    || integer(lot.version) !== heartbeat.authoritativeLotVersion
    || lot.status !== heartbeat.status;
}

function normalizePublicSnapshot(input: unknown): RoomSnapshotPublicV1 {
  const raw = record(input, 'public snapshot');
  return {
    roomId: text(raw.roomId), lotId: text(raw.lotId),
    lotVersion: integer(raw.lotVersion), status: lotStatus(raw.status),
    currentPriceFen: integer(raw.currentPriceFen), endsAtUnixMs: integer(raw.endsAtUnixMs),
    bidCount: integer(raw.bidCount), topRanking: array(raw.topRanking).map((value) => {
      const item = record(value, 'public ranking');
      return { rank: integer(item.rank), maskedNickname: text(item.maskedNickname), maskedAvatarUrl: text(item.maskedAvatarUrl), amountFen: integer(item.amountFen), bidAtUnixMs: integer(item.bidAtUnixMs) };
    }),
  };
}

function normalizeAdminSnapshot(input: unknown): RoomSnapshotAdminV1 {
  const raw = record(input, 'admin snapshot');
  return {
    mainAccountId: text(raw.mainAccountId), roomId: text(raw.roomId), lotId: text(raw.lotId),
    lotVersion: integer(raw.lotVersion), status: lotStatus(raw.status), currentPriceFen: integer(raw.currentPriceFen),
    endsAtUnixMs: integer(raw.endsAtUnixMs), topRanking: array(raw.topRanking).map((value) => {
      const item = record(value, 'admin ranking');
      return { rank: integer(item.rank), userId: text(item.userId), nickname: text(item.nickname), avatarUrl: text(item.avatarUrl), amountFen: integer(item.amountFen), bidAtUnixMs: integer(item.bidAtUnixMs) };
    }),
  };
}

function normalizePersonalDelta(input: unknown): PersonalDeltaV1 {
  const raw = record(input, 'personal delta');
  const visibility = text(raw.orderVisibility) as PersonalDeltaV1['orderVisibility'];
  return {
    userId: text(raw.userId), lotId: text(raw.lotId), lotVersion: integer(raw.lotVersion),
    yourRank: optionalInteger(raw.yourRank), yourAmountFen: optionalInteger(raw.yourAmountFen),
    youAreLeading: Boolean(raw.youAreLeading), yourOrderId: optionalText(raw.yourOrderId),
    orderVisibility: visibility || 'ORDER_VISIBILITY_NONE', tombstone: Boolean(raw.tombstone),
  };
}

function normalizeHeartbeat(input: unknown): RoomHeartbeatV1 {
  const raw = record(input, 'heartbeat');
  return { lotId: text(raw.lotId), authoritativeLotVersion: integer(raw.authoritativeLotVersion), status: lotStatus(raw.status), serverTimeUnixMs: integer(raw.serverTimeUnixMs) };
}

function eventTypeForStatus(status: LotStatus): EventType {
  if (status === 'LOT_STATUS_SETTLED') return 'AUCTION_EVENT_TYPE_LOT_SETTLED';
  if (status === 'LOT_STATUS_CANCELLED') return 'AUCTION_EVENT_TYPE_LOT_CANCELLED';
  if (status === 'LOT_STATUS_FAILED') return 'AUCTION_EVENT_TYPE_AUCTION_CLOSED';
  if (status === 'LOT_STATUS_EXTENDED') return 'AUCTION_EVENT_TYPE_AUCTION_EXTENDED';
  return 'AUCTION_EVENT_TYPE_LOT_UPDATED';
}

function isTerminal(status: LotStatus) {
  return status === 'LOT_STATUS_SETTLED' || status === 'LOT_STATUS_CANCELLED' || status === 'LOT_STATUS_FAILED';
}

function money(amount: number): Money { return { amount, currency: 'CNY' }; }
function record(value: unknown, name: string): Record<string, unknown> { if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${name} 格式错误`); return value as Record<string, unknown>; }
function array(value: unknown): unknown[] { return Array.isArray(value) ? value : []; }
function text(value: unknown): string { return typeof value === 'string' ? value : ''; }
function optionalText(value: unknown): string | undefined { const result = text(value); return result || undefined; }
function integer(value: unknown): number { const result = Number(value ?? 0); if (!Number.isSafeInteger(result)) throw new Error('实时消息包含无效整数'); return result; }
function optionalInteger(value: unknown): number | undefined { return value === undefined || value === null ? undefined : integer(value); }
function lotStatus(value: unknown): LotStatus { const status = text(value) as LotStatus; if (!LOT_STATUSES.has(status)) throw new Error(`无效拍品状态：${status}`); return status; }
