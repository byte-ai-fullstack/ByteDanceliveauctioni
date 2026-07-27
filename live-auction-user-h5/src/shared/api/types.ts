export const RESULT_CODE = {
  OK: 0,
  INVALID_ARGUMENT: 400001,
  LOGIN_REQUIRED: 401001,
  TOKEN_EXPIRED: 401002,
  TOKEN_INVALID: 401003,
  SESSION_EXPIRED: 401004,
  INVALID_CREDENTIALS: 401005,
  FORBIDDEN: 403001,
  ACCOUNT_DISABLED: 403002,
  USER_NOT_FOUND: 404001,
  LOT_VERSION_CONFLICT: 409001,
  USERNAME_TAKEN: 409002,
  ROOM_ACTIVE_LOT_EXISTS: 409003,
  QUEUE_POSITION_CONFLICT: 409004,
  BID_TOO_LOW: 409101,
  BID_NOT_LIVE: 409102,
  BID_ENDED: 409103,
  BID_ALREADY_LEADING: 409104,
  BID_CURRENCY_MISMATCH: 409105,
  BID_VERSION_STALE: 409106,
  LOT_CANCELLED: 409107,
  PROJECTION_PENDING: 409108,
  DEPOSIT_REQUIRED: 409109,
  ADDRESS_REQUIRED: 409110,
  ADDRESS_NOT_FOUND: 409111,
  IDEMPOTENCY_CONFLICT: 409010,
  PAYMENT_PROVIDER_NOT_CONFIGURED: 500101,
  INTERNAL_ERROR: 500000,
} as const;

export type ResultCode = (typeof RESULT_CODE)[keyof typeof RESULT_CODE] | number;

export type ReplyResult = {
  code?: ResultCode | string;
  message?: string;
  traceId?: string;
  trace_id?: string;
};

export type Money = {
  amount: number | string;
  currency: string;
};

export type MoneyInput = Money | number | string | null | undefined;

export type RoomStatus = 'ACTIVE' | 'DISABLED' | string;

export type Room = {
  id: string;
  mainAccountId?: string;
  name: string;
  platform?: string;
  platformRoomId?: string;
  status?: RoomStatus;
  createdByUserId?: string;
  createdAtUnixMs?: number | string;
  updatedAtUnixMs?: number | string;
};

export const LOT_STATUS = {
  UNSPECIFIED: 'LOT_STATUS_UNSPECIFIED',
  DRAFT: 'LOT_STATUS_DRAFT',
  READY: 'LOT_STATUS_READY',
  QUEUED: 'LOT_STATUS_QUEUED',
  LIVE: 'LOT_STATUS_LIVE',
  EXTENDED: 'LOT_STATUS_EXTENDED',
  SETTLED: 'LOT_STATUS_SETTLED',
  CANCELLED: 'LOT_STATUS_CANCELLED',
  FAILED: 'LOT_STATUS_FAILED',
} as const;

export type LotStatus = (typeof LOT_STATUS)[keyof typeof LOT_STATUS];

export const LOT_QUEUE_STATUS = {
  UNSPECIFIED: 'LOT_QUEUE_STATUS_UNSPECIFIED',
  NONE: 'LOT_QUEUE_STATUS_NONE',
  QUEUED: 'LOT_QUEUE_STATUS_QUEUED',
  NEXT: 'LOT_QUEUE_STATUS_NEXT',
} as const;

export type LotQueueStatus = (typeof LOT_QUEUE_STATUS)[keyof typeof LOT_QUEUE_STATUS];

export const TRUST_CARD_TYPE = {
  UNSPECIFIED: 'TRUST_CARD_TYPE_UNSPECIFIED',
  CERTIFICATE: 'TRUST_CARD_TYPE_CERTIFICATE',
  FLAW: 'TRUST_CARD_TYPE_FLAW',
  DETAIL: 'TRUST_CARD_TYPE_DETAIL',
  SERVICE: 'TRUST_CARD_TYPE_SERVICE',
  PRICE_REF: 'TRUST_CARD_TYPE_PRICE_REF',
} as const;

export type TrustCardType = (typeof TRUST_CARD_TYPE)[keyof typeof TRUST_CARD_TYPE];

export const AUCTION_EVENT_TYPE = {
  UNSPECIFIED: 'AUCTION_EVENT_TYPE_UNSPECIFIED',
  ROOM_SNAPSHOT: 'AUCTION_EVENT_TYPE_ROOM_SNAPSHOT',
  LOT_CREATED: 'AUCTION_EVENT_TYPE_LOT_CREATED',
  LOT_STARTED: 'AUCTION_EVENT_TYPE_LOT_STARTED',
  LOT_UPDATED: 'AUCTION_EVENT_TYPE_LOT_UPDATED',
  BID_ACCEPTED: 'AUCTION_EVENT_TYPE_BID_ACCEPTED',
  BID_REJECTED: 'AUCTION_EVENT_TYPE_BID_REJECTED',
  RANKING_UPDATED: 'AUCTION_EVENT_TYPE_RANKING_UPDATED',
  TRUST_REVEALED: 'AUCTION_EVENT_TYPE_TRUST_REVEALED',
  DUEL_STARTED: 'AUCTION_EVENT_TYPE_DUEL_STARTED',
  DUEL_ENDED: 'AUCTION_EVENT_TYPE_DUEL_ENDED',
  LOT_SETTLED: 'AUCTION_EVENT_TYPE_LOT_SETTLED',
  LOT_CANCELLED: 'AUCTION_EVENT_TYPE_LOT_CANCELLED',
  LOT_QUEUED: 'AUCTION_EVENT_TYPE_LOT_QUEUED',
  BID_OUTBID: 'AUCTION_EVENT_TYPE_BID_OUTBID',
  AUCTION_EXTENDED: 'AUCTION_EVENT_TYPE_AUCTION_EXTENDED',
  AUCTION_CLOSED: 'AUCTION_EVENT_TYPE_AUCTION_CLOSED',
  ORDER_CREATED: 'AUCTION_EVENT_TYPE_ORDER_CREATED',
  PAYMENT_SUCCESS: 'AUCTION_EVENT_TYPE_PAYMENT_SUCCESS',
  CLIENT_HEARTBEAT: 'CLIENT_HEARTBEAT',
  SERVER_HEARTBEAT: 'SERVER_HEARTBEAT',
} as const;

export type AuctionEventType = (typeof AUCTION_EVENT_TYPE)[keyof typeof AUCTION_EVENT_TYPE];

export const ROLE_CODE = {
  MERCHANT_OWNER: 'merchant_owner',
  ANCHOR: 'anchor',
  OPERATOR: 'operator',
  BUYER: 'buyer',
} as const;

export type RoleCode = (typeof ROLE_CODE)[keyof typeof ROLE_CODE];

export const PERMISSION_CODE = {
  BID_PLACE: 'bid.place',
  ORDER_PAY: 'order.pay',
  ORDER_VIEW_OWN: 'order.view_own',
} as const;

export type PermissionCode = (typeof PERMISSION_CODE)[keyof typeof PERMISSION_CODE];

export const USER_STATUS = {
  UNSPECIFIED: 'USER_STATUS_UNSPECIFIED',
  ACTIVE: 'USER_STATUS_ACTIVE',
  DISABLED: 'USER_STATUS_DISABLED',
} as const;

export type UserStatus = (typeof USER_STATUS)[keyof typeof USER_STATUS];

export type User = {
  id: string;
  username: string;
  nickname?: string;
  avatarUrl?: string;
  roleCodes: RoleCode[];
  permissionCodes: PermissionCode[];
  mainAccountId: string;
  createdByUserId: string;
  status: UserStatus;
  createdAtUnixMs?: number | string;
  updatedAtUnixMs?: number | string;
};

export function hasPermission(user: Pick<User, 'permissionCodes'> | undefined | null, permissionCode: PermissionCode | string): boolean {
  return Boolean(user?.permissionCodes?.some((item) => item === permissionCode));
}

export function isBuyerUser(user?: Pick<User, 'permissionCodes' | 'status'> | null): boolean {
  return Boolean(user && hasPermission(user, PERMISSION_CODE.BID_PLACE) && user.status === USER_STATUS.ACTIVE);
}

export type AuthTokens = {
  accessToken: string;
  refreshToken: string;
  accessExpiresAtUnixMs: number | string;
  refreshExpiresAtUnixMs: number | string;
};

export type BidRule = {
  startPrice: Money;
  minIncrement: Money;
  capPrice?: Money;
  durationSeconds: number;
  antiSnipeWindowSeconds: number;
  antiSnipeExtendSeconds: number;
  maxExtendCount?: number;
};

export type LotStats = {
  participantCount: number;
  bidCount: number;
};

export type TrustRevealCard = {
  id: string;
  lotId?: string;
  type: TrustCardType | string;
  title?: string;
  content?: string;
  imageUrl?: string;
  revealed?: boolean;
  revealedAtUnixMs?: number | string;
};

export type Lot = {
  id: string;
  roomId: string;
  title: string;
  description?: string;
  imageUrl?: string;
  galleryImageUrls?: string[];
  status: LotStatus;
  currentPrice: Money;
  leadingUserId?: string;
  leadingNickname?: string;
  winnerUserId?: string;
  winnerNickname?: string;
  finalPrice?: Money;
  stats: LotStats;
  startedAtUnixMs?: number | string;
  endsAtUnixMs?: number | string;
  settledAtUnixMs?: number | string;
  createdAtUnixMs?: number | string;
  updatedAtUnixMs?: number | string;
  rule: BidRule;
  version?: number | string;
  queueStatus?: LotQueueStatus;
  queuePosition?: number;
  cancelReason?: string;
  cancelledAtUnixMs?: number | string;
  depositAmount?: Money;
  trustCards?: TrustRevealCard[];
};

export type RankingItem = {
  userId: string;
  nickname?: string;
  avatarUrl?: string;
  amount: Money;
  rank: number;
  isMe?: boolean;
  bidAtUnixMs?: number | string;
};

export type BidEvent = {
  id?: string;
  lotId?: string;
  userId: string;
  nickname?: string;
  avatarUrl?: string;
  amount: Money;
  accepted?: boolean;
  rejectReason?: string;
  createdAtUnixMs?: number | string;
};

export type BidRecord = {
  id: string;
  lotId?: string;
  roomId?: string;
  lotTitle?: string;
  lotImageUrl?: string;
  userId: string;
  nickname?: string;
  amount: Money;
  createdAtUnixMs?: number | string;
  lotStatus?: LotStatus;
  auctionState?: string;
  won?: boolean;
};

export type PageQuery = {
  page?: number;
  pageSize?: number;
};

export type MyOrdersQuery = PageQuery & {
  status?: OrderStatus;
  lotId?: string;
};

export type MyBidsQuery = PageQuery & {
  lotId?: string;
};

export type RoomSnapshot = {
  roomId: string;
  roomName?: string;
  anchorName?: string;
  liveSourceUrl?: string;
  liveStartedAtUnixMs?: number | string;
  onlineCount?: number;
  serverTimeUnixMs?: number | string;
  currentLot?: Lot | null;
  ranking?: RankingItem[];
  recentBids?: BidEvent[];
  playbookStage?: string | number;
};

export type OrderStatus = 'CREATED' | 'PENDING_PAYMENT' | 'PAID' | 'CANCELLED' | 'EXPIRED' | 'REFUNDED' | string;
export type PaymentStatus = 'INIT' | 'PROCESSING' | 'SUCCESS' | 'FAILED' | 'CLOSED' | string;

export type OrderSummary = {
  id: string;
  lotId?: string;
  roomId?: string;
  lotTitle?: string;
  lotImageUrl?: string;
  buyerUserId?: string;
  buyerNickname?: string;
  amount: Money;
  status?: OrderStatus;
  paymentStatus?: PaymentStatus;
  paymentId?: string;
  createdAtUnixMs?: number | string;
  updatedAtUnixMs?: number | string;
  expiresAtUnixMs?: number | string;
  paidAtUnixMs?: number | string;
};

export type PaymentSummary = {
  id: string;
  orderId: string;
  status?: PaymentStatus;
  amount: Money;
  createdAtUnixMs?: number | string;
  succeededAtUnixMs?: number | string;
};

export type OrderList = {
  orders: OrderSummary[];
  total: number;
  page: number;
  pageSize: number;
};

export type BidRecordList = {
  bids: BidRecord[];
  total: number;
  page: number;
  pageSize: number;
};

export type LocalOptimisticState = {
  pendingBid?: {
    lotId: string;
    amount: Money;
    idempotencyKey: string;
    createdAtUnixMs: number;
  };
  pendingPayment?: {
    orderId: string;
    idempotencyKey: string;
    createdAtUnixMs: number;
  };
};

export type AuctionRoomState = {
  roomId: string;
  snapshot: RoomSnapshot | null;
  eventState: {
    lastEvent: AuctionSocketEvent | null;
    source: 'snapshot' | 'websocket' | 'local' | 'init';
  };
  localOptimistic: LocalOptimisticState;
  currentLot: Lot | null;
  ranking: RankingItem[];
  recentBids: BidEvent[];
  serverTimeUnixMs: number | string;
  serverTimeReceivedAtUnixMs?: number;
  lastEventId?: string;
  eventSequence?: number | string;
  activeOrder?: OrderSummary;
  orders: OrderSummary[];
  payment?: PaymentSummary;
  paidLotIds: Record<string, boolean>;
};

export type PlaceBidRequest = {
  amount: Money;
  clientKnownVersion?: number | string;
  idempotencyKey: string;
};

export type PlaceBidResponse = {
  accepted: boolean;
  lot?: Lot;
  ranking?: RankingItem[];
  rejectReason?: string;
  bid?: BidEvent;
};

export type LotResult = {
  lot: Lot;
  order?: OrderSummary;
  orderId?: string;
  winnerUserId?: string;
  winnerNickname?: string;
  finalPrice?: Money;
  auctionState?: string;
};

export type AuctionSocketEvent = {
  id?: string;
  type: AuctionEventType;
  roomId?: string;
  lotId?: string;
  occurredAtUnixMs?: number | string;
  snapshot?: RoomSnapshot;
  lot?: Lot;
  ranking?: RankingItem[];
  bid?: BidEvent;
  recentBids?: BidEvent[];
  rejectReason?: string;
  reason?: string;
  serverTimeUnixMs?: number | string;
  order?: OrderSummary;
  payment?: PaymentSummary;
};

export type AIBuyerResult = {
  type: string;
  title: string;
  roomId: string;
  lotId: string;
  status: string;
  currentPrice?: Money;
  href: string;
  reason: string;
  imageUrl?: string;
};

export type AIBuyerSuggestion = {
  text: string;
  reason?: string;
};

export type AIBuyerSuggestionsReply = {
  result?: ReplyResult;
  suggestions: AIBuyerSuggestion[];
  fallbackUsed: boolean;
};

export type AISource = {
  type: string;
  title: string;
  roomId?: string;
  lotId?: string;
};

export type AIBuyerConsultRequest = {
  query: string;
  roomId?: string;
  lotId?: string;
  budget?: number;
  riskPreference?: string;
};

export type AIBuyerConsultReply = {
  result?: ReplyResult;
  answer: string;
  intent: string;
  results: AIBuyerResult[];
  sources: AISource[];
  fallbackUsed: boolean;
};
