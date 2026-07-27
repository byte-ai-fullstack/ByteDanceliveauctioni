import {
  AUCTION_EVENT_TYPE,
  LOT_QUEUE_STATUS,
  LOT_STATUS,
  PERMISSION_CODE,
  ROLE_CODE,
  TRUST_CARD_TYPE,
  USER_STATUS,
  type AuctionEventType,
  type AuctionSocketEvent,
  type AuthTokens,
  type BidEvent,
  type BidRecord,
  type BidRule,
  type Lot,
  type LotQueueStatus,
  type LotResult,
  type LotStatus,
  type Money,
  type MoneyInput,
  type OrderSummary,
  type PaymentSummary,
  type PlaceBidResponse,
  type RankingItem,
  type Room,
  type RoomSnapshot,
  type TrustCardType,
  type TrustRevealCard,
  type User,
  type PermissionCode,
  type RoleCode,
  type UserStatus,
} from './types';

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord {
  return value && typeof value === 'object' ? (value as RawRecord) : {};
}

function pick<T = unknown>(record: RawRecord, camel: string, snake?: string): T | undefined {
  return (record[camel] ?? (snake ? record[snake] : undefined)) as T | undefined;
}

function stringValue(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback;
}

function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value
    .map((item) => stringValue(item).trim())
    .filter(Boolean);
}

function numberValue(value: unknown, fallback = 0): number {
  const next = Number(value);
  return Number.isFinite(next) ? next : fallback;
}

const roleCodeValues = new Set<RoleCode>(Object.values(ROLE_CODE));
const permissionCodeValues = new Set<PermissionCode>(Object.values(PERMISSION_CODE));
const userStatusValues = new Set<UserStatus>(Object.values(USER_STATUS));
const lotStatusValues = new Set<LotStatus>(Object.values(LOT_STATUS));
const lotQueueStatusValues = new Set<LotQueueStatus>(Object.values(LOT_QUEUE_STATUS));
const trustCardTypeValues = new Set<TrustCardType>(Object.values(TRUST_CARD_TYPE));
const auctionEventTypeValues = new Set<AuctionEventType>(Object.values(AUCTION_EVENT_TYPE));

function normalizeEnum<T extends string>(value: unknown, values: Set<T>, fallback: T): T {
  return typeof value === 'string' && values.has(value as T) ? value as T : fallback;
}

function normalizeEnumArray<T extends string>(value: unknown, values: Set<T>): T[] {
  return Array.isArray(value) ? value.filter((item): item is T => typeof item === 'string' && values.has(item as T)) : [];
}

export function normalizeMoney(value: MoneyInput): Money {
  if (value && typeof value === 'object' && 'amount' in value) {
    const money = value as Money;
    return { amount: money.amount ?? 0, currency: money.currency || 'CNY' };
  }
  return { amount: typeof value === 'number' || typeof value === 'string' ? value : 0, currency: 'CNY' };
}

function normalizeMoneyFields(raw: RawRecord): Money {
  if (raw.amount && typeof raw.amount === 'object') return normalizeMoney(raw.amount as MoneyInput);
  const totalAmount = pick(raw, 'totalAmount', 'total_amount');
  if (typeof totalAmount === 'number' || typeof totalAmount === 'string') {
    return { amount: totalAmount, currency: stringValue(raw.currency, 'CNY') };
  }
  return {
    amount: typeof raw.amount === 'number' || typeof raw.amount === 'string' ? raw.amount : 0,
    currency: stringValue(raw.currency, 'CNY'),
  };
}

function normalizeAuctionOrderStatus(value: unknown): string {
  const status = stringValue(value);
  return status.includes('_') || status === status.toLowerCase() ? status.toUpperCase() : status;
}

export function normalizeAuthTokens(input: unknown): AuthTokens {
  const raw = asRecord(input);
  return {
    accessToken: stringValue(pick(raw, 'accessToken', 'access_token')),
    refreshToken: stringValue(pick(raw, 'refreshToken', 'refresh_token')),
    accessExpiresAtUnixMs: pick(raw, 'accessExpiresAtUnixMs', 'access_expires_at_unix_ms') ?? 0,
    refreshExpiresAtUnixMs: pick(raw, 'refreshExpiresAtUnixMs', 'refresh_expires_at_unix_ms') ?? 0,
  };
}

export function normalizeUser(input: unknown): User {
  const raw = asRecord(input);
  return {
    id: stringValue(raw.id),
    username: stringValue(raw.username),
    nickname: stringValue(raw.nickname),
    avatarUrl: stringValue(pick(raw, 'avatarUrl', 'avatar_url')),
    roleCodes: normalizeEnumArray(pick(raw, 'roleCodes', 'role_codes'), roleCodeValues),
    permissionCodes: normalizeEnumArray(pick(raw, 'permissionCodes', 'permission_codes'), permissionCodeValues),
    mainAccountId: stringValue(pick(raw, 'mainAccountId', 'main_account_id')),
    createdByUserId: stringValue(pick(raw, 'createdByUserId', 'created_by_user_id')),
    status: normalizeUserStatus(pick(raw, 'status')),
    createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
    updatedAtUnixMs: pick(raw, 'updatedAtUnixMs', 'updated_at_unix_ms'),
  };
}

export function normalizeRoom(input: unknown): Room {
  const raw = asRecord(input);
  return {
    id: stringValue(raw.id),
    mainAccountId: stringValue(pick(raw, 'mainAccountId', 'main_account_id')),
    name: stringValue(raw.name),
    platform: stringValue(raw.platform),
    platformRoomId: stringValue(pick(raw, 'platformRoomId', 'platform_room_id')),
    status: stringValue(raw.status),
    createdByUserId: stringValue(pick(raw, 'createdByUserId', 'created_by_user_id')),
    createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
    updatedAtUnixMs: pick(raw, 'updatedAtUnixMs', 'updated_at_unix_ms'),
  };
}

function normalizeUserStatus(value: unknown): UserStatus {
  return normalizeEnum(value, userStatusValues, USER_STATUS.UNSPECIFIED);
}

export function normalizeLotStatus(value: unknown): LotStatus {
  return normalizeEnum(value, lotStatusValues, LOT_STATUS.UNSPECIFIED);
}

function normalizeLotQueueStatus(value: unknown): LotQueueStatus {
  return normalizeEnum(value, lotQueueStatusValues, LOT_QUEUE_STATUS.UNSPECIFIED);
}

function normalizeBidRule(input: unknown): BidRule {
  const raw = asRecord(input);
  return {
    startPrice: normalizeMoney(pick(raw, 'startPrice', 'start_price')),
    minIncrement: normalizeMoney(pick(raw, 'minIncrement', 'min_increment')),
    capPrice: pick(raw, 'capPrice', 'cap_price') ? normalizeMoney(pick(raw, 'capPrice', 'cap_price')) : undefined,
    durationSeconds: numberValue(pick(raw, 'durationSeconds', 'duration_seconds')),
    antiSnipeWindowSeconds: numberValue(pick(raw, 'antiSnipeWindowSeconds', 'anti_snipe_window_seconds')),
    antiSnipeExtendSeconds: numberValue(pick(raw, 'antiSnipeExtendSeconds', 'anti_snipe_extend_seconds')),
    maxExtendCount: numberValue(pick(raw, 'maxExtendCount', 'max_extend_count')),
  };
}

function normalizeLotStats(input: unknown): Lot['stats'] {
  const raw = asRecord(input);
  return {
    participantCount: numberValue(pick(raw, 'participantCount', 'participant_count')),
    bidCount: numberValue(pick(raw, 'bidCount', 'bid_count')),
  };
}

function normalizeTrustRevealCard(input: unknown): TrustRevealCard {
  const raw = asRecord(input);
  return {
    id: stringValue(raw.id),
    lotId: stringValue(pick(raw, 'lotId', 'lot_id')),
    type: normalizeEnum(pick(raw, 'type'), trustCardTypeValues, TRUST_CARD_TYPE.UNSPECIFIED),
    title: stringValue(raw.title),
    content: stringValue(raw.content),
    imageUrl: stringValue(pick(raw, 'imageUrl', 'image_url')),
    revealed: Boolean(raw.revealed),
    revealedAtUnixMs: pick(raw, 'revealedAtUnixMs', 'revealed_at_unix_ms'),
  };
}

export function normalizeLot(input: unknown): Lot {
  const raw = asRecord(input);
  return {
    id: stringValue(raw.id),
    roomId: stringValue(pick(raw, 'roomId', 'room_id')),
    title: stringValue(raw.title),
    description: stringValue(raw.description),
    imageUrl: stringValue(pick(raw, 'imageUrl', 'image_url')),
    galleryImageUrls: stringArray(pick(raw, 'galleryImageUrls', 'gallery_image_urls')),
    status: normalizeLotStatus(raw.status),
    currentPrice: normalizeMoney(pick(raw, 'currentPrice', 'current_price')),
    leadingUserId: stringValue(pick(raw, 'leadingUserId', 'leading_user_id')),
    leadingNickname: stringValue(pick(raw, 'leadingNickname', 'leading_nickname')),
    winnerUserId: stringValue(pick(raw, 'winnerUserId', 'winner_user_id')),
    winnerNickname: stringValue(pick(raw, 'winnerNickname', 'winner_nickname')),
    finalPrice: pick(raw, 'finalPrice', 'final_price') ? normalizeMoney(pick(raw, 'finalPrice', 'final_price')) : undefined,
    stats: normalizeLotStats(pick(raw, 'stats')),
    startedAtUnixMs: pick(raw, 'startedAtUnixMs', 'started_at_unix_ms'),
    endsAtUnixMs: pick(raw, 'endsAtUnixMs', 'ends_at_unix_ms'),
    settledAtUnixMs: pick(raw, 'settledAtUnixMs', 'settled_at_unix_ms'),
    createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
    updatedAtUnixMs: pick(raw, 'updatedAtUnixMs', 'updated_at_unix_ms'),
    rule: normalizeBidRule(raw.rule),
    version: raw.version as Lot['version'],
    queueStatus: normalizeLotQueueStatus(pick(raw, 'queueStatus', 'queue_status')),
    queuePosition: numberValue(pick(raw, 'queuePosition', 'queue_position')),
    cancelReason: stringValue(pick(raw, 'cancelReason', 'cancel_reason')),
    cancelledAtUnixMs: pick(raw, 'cancelledAtUnixMs', 'cancelled_at_unix_ms'),
    depositAmount: pick(raw, 'depositAmount', 'deposit_amount') ? normalizeMoney(pick(raw, 'depositAmount', 'deposit_amount')) : undefined,
    trustCards: Array.isArray(pick(raw, 'trustCards', 'trust_cards'))
      ? (pick<unknown[]>(raw, 'trustCards', 'trust_cards') ?? []).map(normalizeTrustRevealCard)
      : [],
  };
}

export function normalizeRankingItem(input: unknown): RankingItem {
  const raw = asRecord(input);
  return {
    rank: numberValue(raw.rank),
    userId: stringValue(pick(raw, 'userId', 'user_id')),
    nickname: stringValue(raw.nickname),
    avatarUrl: stringValue(pick(raw, 'avatarUrl', 'avatar_url')),
    amount: normalizeMoney(raw.amount as MoneyInput),
    bidAtUnixMs: pick(raw, 'bidAtUnixMs', 'bid_at_unix_ms'),
  };
}

export function normalizeBid(input: unknown): BidEvent {
  const raw = asRecord(input);
  return {
    id: stringValue(raw.id),
    lotId: stringValue(pick(raw, 'lotId', 'lot_id')),
    userId: stringValue(pick(raw, 'userId', 'user_id')),
    nickname: stringValue(raw.nickname),
    avatarUrl: stringValue(pick(raw, 'avatarUrl', 'avatar_url')),
    amount: normalizeMoney(raw.amount as MoneyInput),
    accepted: raw.accepted as boolean | undefined,
    rejectReason: stringValue(pick(raw, 'rejectReason', 'reject_reason')),
    createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
  };
}

export function normalizeRoomSnapshot(input: unknown, fallbackRoomId = ''): RoomSnapshot {
  const raw = asRecord(input);
  return {
    roomId: stringValue(pick(raw, 'roomId', 'room_id'), fallbackRoomId),
    roomName: stringValue(pick(raw, 'roomName', 'room_name')),
    anchorName: stringValue(pick(raw, 'anchorName', 'anchor_name')),
    liveSourceUrl: stringValue(pick(raw, 'liveSourceUrl', 'live_source_url')),
    liveStartedAtUnixMs: pick(raw, 'liveStartedAtUnixMs', 'live_started_at_unix_ms'),
    onlineCount: numberValue(pick(raw, 'onlineCount', 'online_count')),
    serverTimeUnixMs: pick(raw, 'serverTimeUnixMs', 'server_time_unix_ms'),
    currentLot: pick(raw, 'currentLot', 'current_lot') ? normalizeLot(pick(raw, 'currentLot', 'current_lot')) : null,
    ranking: Array.isArray(raw.ranking) ? raw.ranking.map(normalizeRankingItem) : [],
    recentBids: Array.isArray(pick(raw, 'recentBids', 'recent_bids'))
      ? (pick<unknown[]>(raw, 'recentBids', 'recent_bids') ?? []).map(normalizeBid)
      : [],
    playbookStage: pick(raw, 'playbookStage', 'playbook_stage'),
  };
}

function normalizeEventType(value: unknown): AuctionEventType {
  return normalizeEnum(value, auctionEventTypeValues, AUCTION_EVENT_TYPE.UNSPECIFIED);
}

export function normalizeOrder(input: unknown): OrderSummary | undefined {
  const raw = asRecord(input);
  const id = stringValue(raw.id);
  if (!id) return undefined;
  const items = Array.isArray(raw.items) ? raw.items.map(asRecord) : [];
  const firstItem = items[0] ?? {};
  return {
    id,
    lotId: stringValue(pick(raw, 'lotId', 'lot_id')) || stringValue(pick(firstItem, 'lotId', 'lot_id')),
    roomId: stringValue(pick(raw, 'roomId', 'room_id')) || stringValue(pick(firstItem, 'roomId', 'room_id')),
    lotTitle: stringValue(pick(raw, 'lotTitle', 'lot_title')) || stringValue(pick(raw, 'title')) || stringValue(pick(firstItem, 'title')),
    lotImageUrl: stringValue(pick(raw, 'lotImageUrl', 'lot_image_url')) || stringValue(pick(firstItem, 'imageUrl', 'image_url')),
    buyerUserId: stringValue(pick(raw, 'buyerUserId', 'buyer_user_id')) || stringValue(pick(raw, 'userId', 'user_id')),
    buyerNickname: stringValue(pick(raw, 'buyerNickname', 'buyer_nickname')) || stringValue(raw.nickname),
    amount: normalizeMoneyFields(raw),
    status: normalizeAuctionOrderStatus(raw.status),
    paymentStatus: normalizeAuctionOrderStatus(pick(raw, 'paymentStatus', 'payment_status')),
    paymentId: stringValue(pick(raw, 'paymentId', 'payment_id')),
    createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
    updatedAtUnixMs: pick(raw, 'updatedAtUnixMs', 'updated_at_unix_ms'),
    expiresAtUnixMs: pick(raw, 'expiresAtUnixMs', 'expires_at_unix_ms'),
    paidAtUnixMs: pick(raw, 'paidAtUnixMs', 'paid_at_unix_ms'),
  };
}

export function normalizePayment(input: unknown): PaymentSummary | undefined {
  const raw = asRecord(input);
  const id = stringValue(raw.id);
  if (!id) return undefined;
  return {
    id,
    orderId: stringValue(pick(raw, 'orderId', 'order_id')),
    status: stringValue(raw.status),
    amount: normalizeMoneyFields(raw),
    createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
    succeededAtUnixMs: pick(raw, 'succeededAtUnixMs', 'succeeded_at_unix_ms'),
  };
}

export function normalizeBidRecord(input: unknown): BidRecord {
  const raw = asRecord(input);
  return {
    id: stringValue(raw.id),
    lotId: stringValue(pick(raw, 'lotId', 'lot_id')),
    roomId: stringValue(pick(raw, 'roomId', 'room_id')),
    lotTitle: stringValue(pick(raw, 'lotTitle', 'lot_title')),
    lotImageUrl: stringValue(pick(raw, 'lotImageUrl', 'lot_image_url')),
    userId: stringValue(pick(raw, 'userId', 'user_id')),
    nickname: stringValue(raw.nickname),
    amount: normalizeMoneyFields(raw),
    createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
    lotStatus: normalizeLotStatus(pick(raw, 'lotStatus', 'lot_status')),
    auctionState: stringValue(pick(raw, 'auctionState', 'auction_state')),
    won: Boolean(raw.won),
  };
}

export function normalizeAuctionEvent(input: unknown): AuctionSocketEvent {
  const raw = asRecord(input);
  const snapshotRaw = raw.snapshot;
  const type = normalizeEventType(raw.type);
  const privateResultSignal = type === AUCTION_EVENT_TYPE.ORDER_CREATED || type === AUCTION_EVENT_TYPE.PAYMENT_SUCCESS;
  const order = privateResultSignal ? undefined : normalizeOrder(raw.order);
  const payment = privateResultSignal ? undefined : normalizePayment(raw.payment);
  const reason = stringValue(raw.reason);

  return {
    id: stringValue(raw.id),
    type,
    roomId: stringValue(pick(raw, 'roomId', 'room_id')),
    lotId: stringValue(pick(raw, 'lotId', 'lot_id')),
    occurredAtUnixMs: pick(raw, 'occurredAtUnixMs', 'occurred_at_unix_ms'),
    snapshot: snapshotRaw ? normalizeRoomSnapshot(snapshotRaw) : undefined,
    lot: raw.lot ? normalizeLot(raw.lot) : undefined,
    ranking: Array.isArray(raw.ranking) ? raw.ranking.map(normalizeRankingItem) : undefined,
    bid: raw.bid ? normalizeBid(raw.bid) : undefined,
    recentBids: Array.isArray(pick(raw, 'recentBids', 'recent_bids'))
      ? (pick<unknown[]>(raw, 'recentBids', 'recent_bids') ?? []).map(normalizeBid)
      : undefined,
    rejectReason: stringValue(pick(raw, 'rejectReason', 'reject_reason') ?? raw.reason),
    reason,
    serverTimeUnixMs: pick(raw, 'serverTimeUnixMs', 'server_time_unix_ms'),
    order,
    payment,
  };
}

export function normalizePlaceBidResponse(input: unknown): PlaceBidResponse {
  const raw = asRecord(input);
  return {
    accepted: Boolean(raw.accepted),
    lot: raw.lot ? normalizeLot(raw.lot) : undefined,
    ranking: Array.isArray(raw.ranking) ? raw.ranking.map(normalizeRankingItem) : undefined,
    rejectReason: stringValue(pick(raw, 'rejectReason', 'reject_reason')),
    bid: raw.bid ? normalizeBid(raw.bid) : undefined,
  };
}

export function normalizeLotResult(input: unknown): LotResult {
  const raw = asRecord(input);
  const lot = normalizeLot(raw.lot);
  const order = normalizeOrder(raw.order);
  return {
    lot,
    order,
    orderId: stringValue(pick(raw, 'orderId', 'order_id') ?? order?.id),
    winnerUserId: stringValue(pick(raw, 'winnerUserId', 'winner_user_id')),
    winnerNickname: stringValue(pick(raw, 'winnerNickname', 'winner_nickname')),
    finalPrice: pick(raw, 'finalPrice', 'final_price') ? normalizeMoney(pick(raw, 'finalPrice', 'final_price')) : lot.finalPrice,
    auctionState: stringValue(pick(raw, 'auctionState', 'auction_state')),
  };
}
