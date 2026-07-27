/**
 * Generated from live-auction-bid-backend/api/auction/service/v1/realtime.proto.
 * Source SHA-256: 03fccc895548bd0414f34992729e850ceb18712ca3423cdc908f16ce85d9825f
 * Do not edit by hand.
 */

export type LotStatus =
  | 'LOT_STATUS_UNSPECIFIED'
  | 'LOT_STATUS_DRAFT'
  | 'LOT_STATUS_READY'
  | 'LOT_STATUS_QUEUED'
  | 'LOT_STATUS_LIVE'
  | 'LOT_STATUS_EXTENDED'
  | 'LOT_STATUS_SETTLED'
  | 'LOT_STATUS_CANCELLED'
  | 'LOT_STATUS_FAILED';

type OrderVisibility =
  | 'ORDER_VISIBILITY_UNSPECIFIED'
  | 'ORDER_VISIBILITY_NONE'
  | 'ORDER_VISIBILITY_CREATING'
  | 'ORDER_VISIBILITY_READY';

type PublicRankingItemV1 = {
  rank: number;
  maskedNickname: string;
  maskedAvatarUrl: string;
  amountFen: number;
  bidAtUnixMs: number;
};

type PublicSettlementV1 = {
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
  topRanking: PublicRankingItemV1[];
  settlement?: PublicSettlementV1;
};

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

export type RoomHeartbeatV1 = {
  lotId: string;
  authoritativeLotVersion: number;
  status: LotStatus;
  serverTimeUnixMs: number;
};

type AdminRankingItemV1 = {
  rank: number;
  userId: string;
  nickname: string;
  avatarUrl: string;
  amountFen: number;
  bidAtUnixMs: number;
};

export type RoomSnapshotAdminV1 = {
  mainAccountId: string;
  roomId: string;
  lotId: string;
  lotVersion: number;
  status: LotStatus;
  currentPriceFen: number;
  endsAtUnixMs: number;
  topRanking: AdminRankingItemV1[];
};

type ReconnectControlV1 = {
  retryAfterMs: number;
  reason: string;
};

export type RealtimeEnvelopeV1 = {
  messageId: string;
  schemaVersion: number;
  occurredAtUnixMs: number;
  publicSnapshot?: RoomSnapshotPublicV1;
  personalDelta?: PersonalDeltaV1;
  heartbeat?: RoomHeartbeatV1;
  adminSnapshot?: RoomSnapshotAdminV1;
  reconnect?: ReconnectControlV1;
};
