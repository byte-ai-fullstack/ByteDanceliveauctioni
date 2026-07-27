import {
  AUCTION_EVENT_TYPE,
  LOT_STATUS,
  type AuctionSocketEvent,
  type LotStatus,
  type RankingItem,
  type RoomSnapshot,
} from '../api/types';

export const REALTIME_SCHEMA_VERSION = 1;

export type PublicSettlementV1 = {
  status: LotStatus;
  finalPriceFen: number;
  maskedWinnerNickname: string;
  settledAtUnixMs: number;
  cancelReason: string;
};

export type RoomSnapshotPublicV1 = {
  roomId: string;
  lotId: string;
  lotVersion: number;
  status: LotStatus;
  currentPriceFen: number;
  endsAtUnixMs: number;
  bidCount: number;
  topRanking: Array<{ rank: number; maskedNickname: string; maskedAvatarUrl: string; amountFen: number; bidAtUnixMs: number }>;
  settlement?: PublicSettlementV1;
};

export type OrderVisibility = 'ORDER_VISIBILITY_UNSPECIFIED' | 'ORDER_VISIBILITY_NONE' | 'ORDER_VISIBILITY_CREATING' | 'ORDER_VISIBILITY_READY';
export type PersonalDeltaV1 = {
  userId: string;
  lotId: string;
  lotVersion: number;
  yourRank?: number;
  yourAmountFen?: number;
  youAreLeading: boolean;
  yourOrderId?: string;
  orderVisibility: OrderVisibility;
  tombstone: boolean;
};
export type RoomHeartbeatV1 = { lotId: string; authoritativeLotVersion: number; status: LotStatus; serverTimeUnixMs: number };
export type ReconnectControlV1 = { retryAfterMs: number; reason: string };
export type RealtimeEnvelopeV1 = {
  messageId: string;
  schemaVersion: number;
  occurredAtUnixMs: number;
  publicSnapshot?: RoomSnapshotPublicV1;
  personalDelta?: PersonalDeltaV1;
  heartbeat?: RoomHeartbeatV1;
  reconnect?: ReconnectControlV1;
  adminSnapshot?: unknown;
};

const lotStatuses = new Set<LotStatus>(Object.values(LOT_STATUS));

export function normalizeRealtimeEnvelope(input: unknown): RealtimeEnvelopeV1 {
  const raw = record(input, 'realtime envelope');
  const schemaVersion = integer(raw.schemaVersion ?? raw.schema_version);
  if (schemaVersion !== REALTIME_SCHEMA_VERSION) throw new Error(`unsupported realtime schema ${schemaVersion}`);
  const envelope: RealtimeEnvelopeV1 = {
    messageId: text(raw.messageId ?? raw.message_id),
    schemaVersion,
    occurredAtUnixMs: integer(raw.occurredAtUnixMs ?? raw.occurred_at_unix_ms),
  };
  if (raw.publicSnapshot ?? raw.public_snapshot) envelope.publicSnapshot = publicSnapshot(raw.publicSnapshot ?? raw.public_snapshot);
  if (raw.personalDelta ?? raw.personal_delta) envelope.personalDelta = normalizePersonalDeltaV1(raw.personalDelta ?? raw.personal_delta);
  if (raw.heartbeat) envelope.heartbeat = heartbeat(raw.heartbeat);
  if (raw.reconnect) {
    const value = record(raw.reconnect, 'reconnect');
    envelope.reconnect = { retryAfterMs: integer(value.retryAfterMs ?? value.retry_after_ms), reason: text(value.reason) };
  }
  if (raw.adminSnapshot ?? raw.admin_snapshot) envelope.adminSnapshot = raw.adminSnapshot ?? raw.admin_snapshot;
  if ([envelope.publicSnapshot, envelope.personalDelta, envelope.heartbeat, envelope.reconnect, envelope.adminSnapshot].filter(Boolean).length !== 1) {
    throw new Error('realtime envelope must contain exactly one payload');
  }
  return envelope;
}

export function mergePublicRealtimeState(
  current: RoomSnapshot | null,
  publicState: RoomSnapshotPublicV1,
  personal: PersonalDeltaV1 | undefined,
  occurredAtUnixMs: number,
): RoomSnapshot | null {
  const lot = current?.currentLot;
  if (!current || !lot || lot.id !== publicState.lotId) return null;
  const currentVersion = integer(lot.version);
  if (publicState.lotVersion < currentVersion) return current;

  const ownIdentity = personal ? current.ranking?.find((item) => item.userId === personal.userId) : undefined;
  const ranking: RankingItem[] = publicState.topRanking.map((item) => ({
    rank: item.rank,
    userId: '',
    nickname: item.maskedNickname,
    avatarUrl: item.maskedAvatarUrl,
    amount: money(item.amountFen),
    bidAtUnixMs: item.bidAtUnixMs,
  }));
  if (personal && !personal.tombstone && personal.lotVersion === publicState.lotVersion && personal.yourRank !== undefined) {
    const item = ranking.find((candidate) => candidate.rank === personal.yourRank);
    if (item) {
      item.userId = personal.userId;
      item.nickname = ownIdentity?.nickname || item.nickname;
      item.avatarUrl = ownIdentity?.avatarUrl || item.avatarUrl;
      if (personal.yourAmountFen !== undefined) item.amount = money(personal.yourAmountFen);
    }
  }

  const hasPersonal = Boolean(personal && !personal.tombstone && personal.lotVersion === publicState.lotVersion);
  const isWinner = hasPersonal && (personal?.orderVisibility === 'ORDER_VISIBILITY_CREATING' || personal?.orderVisibility === 'ORDER_VISIBILITY_READY');
  const leading = ranking[0];
  const settlement = publicState.settlement;
  return {
    ...current,
    serverTimeUnixMs: occurredAtUnixMs,
    ranking,
    currentLot: {
      ...lot,
      status: publicState.status,
      version: publicState.lotVersion,
      currentPrice: money(publicState.currentPriceFen),
      endsAtUnixMs: publicState.endsAtUnixMs,
      stats: { ...lot.stats, bidCount: publicState.bidCount },
      leadingUserId: personal?.youAreLeading && hasPersonal ? personal.userId : '',
      leadingNickname: personal?.youAreLeading && hasPersonal ? ownIdentity?.nickname || leading?.nickname : leading?.nickname,
      winnerUserId: isWinner ? personal?.userId : '',
      winnerNickname: isWinner ? ownIdentity?.nickname || settlement?.maskedWinnerNickname : settlement?.maskedWinnerNickname,
      finalPrice: settlement ? money(settlement.finalPriceFen) : lot.finalPrice,
      settledAtUnixMs: settlement?.settledAtUnixMs || lot.settledAtUnixMs,
      cancelReason: settlement?.cancelReason || '',
    },
  };
}

export function publicSnapshotEvent(envelope: RealtimeEnvelopeV1, snapshot: RoomSnapshot, personalUpdate = false): AuctionSocketEvent {
  const publicState = envelope.publicSnapshot;
  const lot = snapshot.currentLot;
  if (!publicState || !lot) throw new Error('public realtime snapshot is incomplete');
  let type: AuctionSocketEvent['type'] = AUCTION_EVENT_TYPE.LOT_UPDATED;
  if (publicState.status === LOT_STATUS.SETTLED) type = AUCTION_EVENT_TYPE.LOT_SETTLED;
  else if (publicState.status === LOT_STATUS.CANCELLED) type = AUCTION_EVENT_TYPE.LOT_CANCELLED;
  else if (publicState.status === LOT_STATUS.FAILED) type = AUCTION_EVENT_TYPE.AUCTION_CLOSED;
  else if (publicState.status === LOT_STATUS.EXTENDED) type = AUCTION_EVENT_TYPE.AUCTION_EXTENDED;
  else if (personalUpdate) type = AUCTION_EVENT_TYPE.RANKING_UPDATED;
  return {
    id: envelope.messageId,
    type,
    roomId: publicState.roomId,
    lotId: publicState.lotId,
    occurredAtUnixMs: envelope.occurredAtUnixMs,
    serverTimeUnixMs: envelope.occurredAtUnixMs,
    lot,
    ranking: snapshot.ranking,
    reason: publicState.settlement?.cancelReason,
  };
}

export function heartbeatRequiresRecovery(snapshot: RoomSnapshot | null, value: RoomHeartbeatV1): boolean {
  const lot = snapshot?.currentLot;
  if (!lot) return value.lotId !== '' || value.authoritativeLotVersion !== 0;
  return lot.id !== value.lotId || integer(lot.version) !== value.authoritativeLotVersion || lot.status !== value.status;
}

function publicSnapshot(input: unknown): RoomSnapshotPublicV1 {
  const raw = record(input, 'public snapshot');
  const settlementRaw = raw.settlement;
  return {
    roomId: text(raw.roomId ?? raw.room_id), lotId: text(raw.lotId ?? raw.lot_id), lotVersion: integer(raw.lotVersion ?? raw.lot_version),
    status: lotStatus(raw.status), currentPriceFen: integer(raw.currentPriceFen ?? raw.current_price_fen), endsAtUnixMs: integer(raw.endsAtUnixMs ?? raw.ends_at_unix_ms),
    bidCount: integer(raw.bidCount ?? raw.bid_count),
    topRanking: array(raw.topRanking ?? raw.top_ranking).map((value) => {
      const item = record(value, 'public ranking');
      return { rank: integer(item.rank), maskedNickname: text(item.maskedNickname ?? item.masked_nickname), maskedAvatarUrl: text(item.maskedAvatarUrl ?? item.masked_avatar_url), amountFen: integer(item.amountFen ?? item.amount_fen), bidAtUnixMs: integer(item.bidAtUnixMs ?? item.bid_at_unix_ms) };
    }),
    settlement: settlementRaw ? settlement(settlementRaw) : undefined,
  };
}

function settlement(input: unknown): PublicSettlementV1 {
  const raw = record(input, 'public settlement');
  return { status: lotStatus(raw.status), finalPriceFen: integer(raw.finalPriceFen ?? raw.final_price_fen), maskedWinnerNickname: text(raw.maskedWinnerNickname ?? raw.masked_winner_nickname), settledAtUnixMs: integer(raw.settledAtUnixMs ?? raw.settled_at_unix_ms), cancelReason: text(raw.cancelReason ?? raw.cancel_reason) };
}

export function normalizePersonalDeltaV1(input: unknown): PersonalDeltaV1 {
  const raw = record(input, 'personal delta');
  return {
    userId: text(raw.userId ?? raw.user_id), lotId: text(raw.lotId ?? raw.lot_id), lotVersion: integer(raw.lotVersion ?? raw.lot_version),
    yourRank: optionalInteger(raw.yourRank ?? raw.your_rank), yourAmountFen: optionalInteger(raw.yourAmountFen ?? raw.your_amount_fen),
    youAreLeading: Boolean(raw.youAreLeading ?? raw.you_are_leading), yourOrderId: optionalText(raw.yourOrderId ?? raw.your_order_id),
    orderVisibility: orderVisibility(raw.orderVisibility ?? raw.order_visibility), tombstone: Boolean(raw.tombstone),
  };
}

function heartbeat(input: unknown): RoomHeartbeatV1 {
  const raw = record(input, 'heartbeat');
  return { lotId: text(raw.lotId ?? raw.lot_id), authoritativeLotVersion: integer(raw.authoritativeLotVersion ?? raw.authoritative_lot_version), status: lotStatus(raw.status), serverTimeUnixMs: integer(raw.serverTimeUnixMs ?? raw.server_time_unix_ms) };
}

function money(amount: number) { return { amount, currency: 'CNY' }; }
function record(value: unknown, name: string): Record<string, unknown> { if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${name} is invalid`); return value as Record<string, unknown>; }
function array(value: unknown): unknown[] { return Array.isArray(value) ? value : []; }
function text(value: unknown): string { return typeof value === 'string' ? value : ''; }
function optionalText(value: unknown): string | undefined { const result = text(value); return result || undefined; }
function integer(value: unknown): number { const result = Number(value ?? 0); if (!Number.isSafeInteger(result)) throw new Error('realtime integer is invalid'); return result; }
function optionalInteger(value: unknown): number | undefined { return value === undefined || value === null ? undefined : integer(value); }
function lotStatus(value: unknown): LotStatus { const status = text(value) as LotStatus; if (!lotStatuses.has(status)) throw new Error(`lot status is invalid: ${status}`); return status; }
function orderVisibility(value: unknown): OrderVisibility {
  const visibility = text(value) || 'ORDER_VISIBILITY_NONE';
  if (!['ORDER_VISIBILITY_UNSPECIFIED', 'ORDER_VISIBILITY_NONE', 'ORDER_VISIBILITY_CREATING', 'ORDER_VISIBILITY_READY'].includes(visibility)) {
    throw new Error(`order visibility is invalid: ${visibility}`);
  }
  return visibility as OrderVisibility;
}
