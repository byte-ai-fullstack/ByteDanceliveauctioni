import type {
  AuctionEvent,
  AuthTokens,
  Bid,
  BidRule,
  DuelState,
  Lot,
  Money,
  RankingItem,
  Room,
  RoomPresence,
  RoomSnapshot,
  TrustRevealCard,
  UploadedAsset,
  User,
} from './types';
import {
  arrayValue,
  asRecord,
  boolValue,
  eventTypeValues,
  field,
  lotQueueStatusValues,
  lotStatusValues,
  normalizeEnum,
  normalizeEnumArray,
  normalizeStringArray,
  numberValue,
  optionalString,
  permissionCodeValues,
  playbookStageValues,
  requiredField,
  roleCodeValues,
  scalarValue,
  stringValue,
  trustCardTypeValues,
  userStatusValues,
} from './normalizerPrimitives';

export function normalizeMoney(input: unknown, name: string): Money {
  if (input === undefined || input === null) return { amount: 0, currency: 'CNY' };
  const raw = asRecord(input, name);
  return {
    amount: scalarValue(field(raw, 'amount'), 0),
    currency: stringValue(field(raw, 'currency') ?? 'CNY') || 'CNY',
  };
}

function normalizeBidRule(input: unknown): BidRule {
  const raw = input === undefined || input === null ? {} : asRecord(input, 'lot.rule');
  return {
    startPrice: normalizeMoney(field(raw, 'startPrice'), 'lot.rule.startPrice'),
    minIncrement: normalizeMoney(field(raw, 'minIncrement'), 'lot.rule.minIncrement'),
    capPrice: field(raw, 'capPrice') === undefined ? undefined : normalizeMoney(field(raw, 'capPrice'), 'lot.rule.capPrice'),
    durationSeconds: numberValue(field(raw, 'durationSeconds')),
    antiSnipeWindowSeconds: numberValue(field(raw, 'antiSnipeWindowSeconds')),
    antiSnipeExtendSeconds: numberValue(field(raw, 'antiSnipeExtendSeconds')),
    maxExtendCount: numberValue(field(raw, 'maxExtendCount')),
  };
}

export function normalizeTrustRevealCard(input: unknown): TrustRevealCard {
  const raw = asRecord(input, 'trustCard');
  return {
    id: stringValue(requiredField(field(raw, 'id'), 'trustCard.id')),
    lotId: stringValue(field(raw, 'lotId')),
    type: normalizeEnum(field(raw, 'type'), 'trustCard.type', trustCardTypeValues, 'TRUST_CARD_TYPE_UNSPECIFIED'),
    title: stringValue(field(raw, 'title')),
    content: stringValue(field(raw, 'content')),
    imageUrl: optionalString(field(raw, 'imageUrl')),
    revealed: boolValue(field(raw, 'revealed')),
    revealedAtUnixMs: scalarValue(field(raw, 'revealedAtUnixMs'), 0),
  };
}

function normalizeBid(input: unknown): Bid {
  const raw = asRecord(input, 'bid');
  return {
    id: stringValue(requiredField(field(raw, 'id'), 'bid.id')),
    lotId: stringValue(field(raw, 'lotId')),
    userId: stringValue(field(raw, 'userId')),
    nickname: stringValue(field(raw, 'nickname')),
    amount: normalizeMoney(field(raw, 'amount'), 'bid.amount'),
    createdAtUnixMs: scalarValue(field(raw, 'createdAtUnixMs'), 0),
  };
}

function normalizeRankingItem(input: unknown): RankingItem {
  const raw = asRecord(input, 'rankingItem');
  return {
    rank: numberValue(field(raw, 'rank')),
    userId: stringValue(field(raw, 'userId')),
    nickname: stringValue(field(raw, 'nickname')),
    amount: normalizeMoney(field(raw, 'amount'), 'rankingItem.amount'),
    bidAtUnixMs: scalarValue(field(raw, 'bidAtUnixMs'), 0),
  };
}

function normalizeDuelState(input: unknown): DuelState {
  const raw = input === undefined || input === null ? {} : asRecord(input, 'duelState');
  return {
    active: boolValue(field(raw, 'active')),
    lotId: stringValue(field(raw, 'lotId')),
    userAId: stringValue(field(raw, 'userAId')),
    userANickname: stringValue(field(raw, 'userANickname')),
    userBId: stringValue(field(raw, 'userBId')),
    userBNickname: stringValue(field(raw, 'userBNickname')),
    startedAtUnixMs: scalarValue(field(raw, 'startedAtUnixMs'), 0),
    endsAtUnixMs: scalarValue(field(raw, 'endsAtUnixMs'), 0),
    extendCount: numberValue(field(raw, 'extendCount')),
    maxExtendCount: numberValue(field(raw, 'maxExtendCount')),
  };
}

function normalizeLotStats(input: unknown): Lot['stats'] {
  const raw = input === undefined || input === null ? {} : asRecord(input, 'lot.stats');
  return {
    participantCount: numberValue(field(raw, 'participantCount')),
    bidCount: numberValue(field(raw, 'bidCount')),
  };
}

export function normalizeLot(input: unknown): Lot {
  const raw = asRecord(input, 'lot');
  const estimatePrice = field(raw, 'estimatePrice');
  return {
    id: stringValue(requiredField(field(raw, 'id'), 'lot.id')),
    roomId: stringValue(field(raw, 'roomId')),
    title: stringValue(field(raw, 'title')),
    description: stringValue(field(raw, 'description')),
    imageUrl: stringValue(field(raw, 'imageUrl')),
    status: normalizeEnum(field(raw, 'status'), 'lot.status', lotStatusValues, 'LOT_STATUS_UNSPECIFIED'),
    queueStatus: field(raw, 'queueStatus') === undefined ? undefined : normalizeEnum(field(raw, 'queueStatus'), 'lot.queueStatus', lotQueueStatusValues),
    queuePosition: field(raw, 'queuePosition') === undefined ? undefined : numberValue(field(raw, 'queuePosition')),
    rule: normalizeBidRule(field(raw, 'rule')),
    currentPrice: normalizeMoney(field(raw, 'currentPrice'), 'lot.currentPrice'),
    leadingUserId: stringValue(field(raw, 'leadingUserId')),
    leadingNickname: stringValue(field(raw, 'leadingNickname')),
    startedAtUnixMs: scalarValue(field(raw, 'startedAtUnixMs'), 0),
    endsAtUnixMs: scalarValue(field(raw, 'endsAtUnixMs'), 0),
    settledAtUnixMs: scalarValue(field(raw, 'settledAtUnixMs'), 0),
    cancelledAtUnixMs: field(raw, 'cancelledAtUnixMs') as number | string | undefined,
    winnerUserId: stringValue(field(raw, 'winnerUserId')),
    winnerNickname: stringValue(field(raw, 'winnerNickname')),
    finalPrice: normalizeMoney(field(raw, 'finalPrice'), 'lot.finalPrice'),
    version: scalarValue(field(raw, 'version'), 0),
    trustCards: arrayValue(field(raw, 'trustCards'), 'lot.trustCards').map(normalizeTrustRevealCard),
    duelState: normalizeDuelState(field(raw, 'duelState')),
    playbookStage: normalizeEnum(field(raw, 'playbookStage'), 'lot.playbookStage', playbookStageValues, 'PLAYBOOK_STAGE_UNSPECIFIED'),
    stats: normalizeLotStats(field(raw, 'stats')),
    cancelReason: optionalString(field(raw, 'cancelReason')),
    galleryImageUrls: normalizeStringArray(field(raw, 'galleryImageUrls'), 'lot.galleryImageUrls'),
    category: optionalString(field(raw, 'category')),
    tags: normalizeStringArray(field(raw, 'tags'), 'lot.tags'),
    estimatePrice: estimatePrice === undefined ? undefined : normalizeMoney(estimatePrice, 'lot.estimatePrice'),
    stock: field(raw, 'stock') as number | string | undefined,
    afterSaleNotes: optionalString(field(raw, 'afterSaleNotes')),
    depositAmount: field(raw, 'depositAmount') === undefined ? undefined : normalizeMoney(field(raw, 'depositAmount'), 'lot.depositAmount'),
  };
}

export function normalizeRoomSnapshot(input: unknown): RoomSnapshot {
  const raw = asRecord(input, 'snapshot');
  const currentLot = field(raw, 'currentLot');
  return {
    roomId: stringValue(requiredField(field(raw, 'roomId'), 'snapshot.roomId')),
    currentLot: currentLot === undefined || currentLot === null ? undefined : normalizeLot(currentLot),
    ranking: arrayValue(field(raw, 'ranking'), 'snapshot.ranking').map(normalizeRankingItem),
    recentBids: arrayValue(field(raw, 'recentBids'), 'snapshot.recentBids').map(normalizeBid),
    playbookStage: normalizeEnum(field(raw, 'playbookStage'), 'snapshot.playbookStage', playbookStageValues, 'PLAYBOOK_STAGE_UNSPECIFIED'),
    serverTimeUnixMs: scalarValue(field(raw, 'serverTimeUnixMs'), 0),
  };
}

export function normalizeRoom(input: unknown): Room {
  const raw = asRecord(input, 'room');
  return {
    id: stringValue(requiredField(field(raw, 'id'), 'room.id')),
    mainAccountId: stringValue(field(raw, 'mainAccountId')),
    name: stringValue(field(raw, 'name')),
    platform: stringValue(field(raw, 'platform')) || 'douyin',
    platformRoomId: optionalString(field(raw, 'platformRoomId')),
    status: stringValue(field(raw, 'status')) || 'ACTIVE',
    createdByUserId: optionalString(field(raw, 'createdByUserId')),
    createdAtUnixMs: scalarValue(field(raw, 'createdAtUnixMs'), 0),
    updatedAtUnixMs: scalarValue(field(raw, 'updatedAtUnixMs'), 0),
  };
}

export function normalizeRoomPresence(input: unknown): RoomPresence {
  const raw = asRecord(input, 'presence');
  return {
    roomId: stringValue(requiredField(field(raw, 'roomId'), 'presence.roomId')),
    totalConnections: requiredField(field(raw, 'totalConnections'), 'presence.totalConnections') as number | string,
    viewerConnections: requiredField(field(raw, 'viewerConnections'), 'presence.viewerConnections') as number | string,
    operatorConnections: requiredField(field(raw, 'operatorConnections'), 'presence.operatorConnections') as number | string,
    serverTimeUnixMs: requiredField(field(raw, 'serverTimeUnixMs'), 'presence.serverTimeUnixMs') as number | string,
  };
}

export function normalizeAuctionEvent(input: unknown): AuctionEvent {
  const raw = asRecord(input, 'event');
  const lot = field(raw, 'lot');
  const bid = field(raw, 'bid');
  const ranking = field(raw, 'ranking');
  const trustCard = field(raw, 'trustCard');
  const duelState = field(raw, 'duelState');
  const snapshot = field(raw, 'snapshot');
  return {
    ...raw,
    id: stringValue(field(raw, 'id')),
    type: normalizeEnum(field(raw, 'type'), 'event.type', eventTypeValues),
    roomId: stringValue(field(raw, 'roomId')),
    lotId: stringValue(field(raw, 'lotId')),
    occurredAtUnixMs: scalarValue(field(raw, 'occurredAtUnixMs'), 0),
    lot: lot === undefined || lot === null ? undefined : normalizeLot(lot),
    bid: bid === undefined || bid === null ? undefined : normalizeBid(bid),
    ranking: ranking === undefined || ranking === null ? undefined : arrayValue(ranking, 'event.ranking').map(normalizeRankingItem),
    trustCard: trustCard === undefined || trustCard === null ? undefined : normalizeTrustRevealCard(trustCard),
    duelState: duelState === undefined || duelState === null ? undefined : normalizeDuelState(duelState),
    snapshot: snapshot === undefined || snapshot === null ? undefined : normalizeRoomSnapshot(snapshot),
    reason: optionalString(field(raw, 'reason')),
    orderId: optionalString(field(raw, 'orderId')),
    paymentId: optionalString(field(raw, 'paymentId')),
  } as AuctionEvent;
}

export function normalizeUser(input: unknown): User {
  const raw = asRecord(input, 'user');
  return {
    id: stringValue(requiredField(field(raw, 'id'), 'user.id')),
    username: stringValue(requiredField(field(raw, 'username'), 'user.username')),
    nickname: stringValue(field(raw, 'nickname')),
    roleCodes: normalizeEnumArray(field(raw, 'roleCodes'), 'user.roleCodes', roleCodeValues),
    permissionCodes: normalizeEnumArray(field(raw, 'permissionCodes'), 'user.permissionCodes', permissionCodeValues),
    mainAccountId: stringValue(field(raw, 'mainAccountId')),
    createdByUserId: stringValue(field(raw, 'createdByUserId')),
    status: normalizeEnum(field(raw, 'status'), 'user.status', userStatusValues),
    createdAtUnixMs: scalarValue(field(raw, 'createdAtUnixMs'), 0),
    updatedAtUnixMs: scalarValue(field(raw, 'updatedAtUnixMs'), 0),
  };
}

export function normalizeAuthTokens(input: unknown): AuthTokens {
  const raw = asRecord(input, 'tokens');
  return {
    accessToken: stringValue(requiredField(field(raw, 'accessToken'), 'tokens.accessToken')),
    refreshToken: stringValue(requiredField(field(raw, 'refreshToken'), 'tokens.refreshToken')),
    accessExpiresAtUnixMs: requiredField(field(raw, 'accessExpiresAtUnixMs'), 'tokens.accessExpiresAtUnixMs') as number | string,
    refreshExpiresAtUnixMs: requiredField(field(raw, 'refreshExpiresAtUnixMs'), 'tokens.refreshExpiresAtUnixMs') as number | string,
  };
}

export function normalizeUploadedAsset(input: unknown): UploadedAsset {
  const raw = asRecord(input, 'asset');
  return {
    id: stringValue(requiredField(field(raw, 'id'), 'asset.id')),
    imageUrl: stringValue(requiredField(field(raw, 'imageUrl'), 'asset.imageUrl')),
    bucket: stringValue(field(raw, 'bucket')),
    objectKey: stringValue(field(raw, 'objectKey')),
    mimeType: stringValue(field(raw, 'mimeType')),
    sizeBytes: scalarValue(field(raw, 'sizeBytes'), 0),
    status: optionalString(field(raw, 'status')),
    expiresAtUnixMs: field(raw, 'expiresAtUnixMs') as number | string | undefined,
  };
}
