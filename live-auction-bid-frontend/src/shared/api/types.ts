import type { components } from './generated/auction.schema';

type ApiSchema<Name extends keyof components['schemas']> = components['schemas'][Name];
type Normalized<Name extends keyof components['schemas'], Fields extends object> = Omit<ApiSchema<Name>, keyof Fields> & Fields;

export type Money = ApiSchema<'Money'>;
export type ReplyResult = ApiSchema<'ReplyResult'>;

export const RESULT_CODE_OK = 0;
export const RESULT_CODE_INVALID_ARGUMENT = 400001;
export const RESULT_CODE_LOGIN_REQUIRED = 401001;
export const RESULT_CODE_TOKEN_EXPIRED = 401002;
export const RESULT_CODE_TOKEN_INVALID = 401003;
export const RESULT_CODE_SESSION_EXPIRED = 401004;
export const RESULT_CODE_INVALID_CREDENTIALS = 401005;
export const RESULT_CODE_FORBIDDEN = 403001;
export const RESULT_CODE_ACCOUNT_DISABLED = 403002;
export const RESULT_CODE_USER_NOT_FOUND = 404001;
export const RESULT_CODE_LOT_VERSION_CONFLICT = 409001;
export const RESULT_CODE_USERNAME_TAKEN = 409002;
export const RESULT_CODE_ROOM_ACTIVE_LOT_EXISTS = 409003;
export const RESULT_CODE_QUEUE_POSITION_CONFLICT = 409004;
export const RESULT_CODE_BID_TOO_LOW = 409101;
export const RESULT_CODE_BID_NOT_LIVE = 409102;
export const RESULT_CODE_BID_ENDED = 409103;
export const RESULT_CODE_BID_ALREADY_LEADING = 409104;
export const RESULT_CODE_BID_CURRENCY_MISMATCH = 409105;
export const RESULT_CODE_BID_VERSION_STALE = 409106;
export const RESULT_CODE_LOT_CANCELLED = 409107;
export const RESULT_CODE_PROJECTION_PENDING = 409108;
export const RESULT_CODE_INTERNAL_ERROR = 500000;

export type LotStatus = ApiSchema<'LotStatus'>;
export type LotQueueStatus = ApiSchema<'LotQueueStatus'>;
export type TrustCardType = 'TRUST_CARD_TYPE_UNSPECIFIED' | 'TRUST_CARD_TYPE_CERTIFICATE' | 'TRUST_CARD_TYPE_FLAW' | 'TRUST_CARD_TYPE_DETAIL' | 'TRUST_CARD_TYPE_SERVICE' | 'TRUST_CARD_TYPE_PRICE_REF';
export type PlaybookStage = 'PLAYBOOK_STAGE_UNSPECIFIED' | 'PLAYBOOK_STAGE_WARM_UP' | 'PLAYBOOK_STAGE_TRUST_BLOCKED' | 'PLAYBOOK_STAGE_BIDDING_ACTIVE' | 'PLAYBOOK_STAGE_DUEL_READY' | 'PLAYBOOK_STAGE_DUEL_MODE' | 'PLAYBOOK_STAGE_SETTLE_READY';
export type EventType = 'AUCTION_EVENT_TYPE_UNSPECIFIED' | 'AUCTION_EVENT_TYPE_ROOM_SNAPSHOT' | 'AUCTION_EVENT_TYPE_LOT_CREATED' | 'AUCTION_EVENT_TYPE_LOT_STARTED' | 'AUCTION_EVENT_TYPE_LOT_UPDATED' | 'AUCTION_EVENT_TYPE_BID_ACCEPTED' | 'AUCTION_EVENT_TYPE_BID_REJECTED' | 'AUCTION_EVENT_TYPE_RANKING_UPDATED' | 'AUCTION_EVENT_TYPE_TRUST_REVEALED' | 'AUCTION_EVENT_TYPE_DUEL_STARTED' | 'AUCTION_EVENT_TYPE_DUEL_ENDED' | 'AUCTION_EVENT_TYPE_LOT_SETTLED' | 'AUCTION_EVENT_TYPE_LOT_CANCELLED' | 'AUCTION_EVENT_TYPE_LOT_QUEUED' | 'AUCTION_EVENT_TYPE_BID_OUTBID' | 'AUCTION_EVENT_TYPE_AUCTION_EXTENDED' | 'AUCTION_EVENT_TYPE_AUCTION_CLOSED' | 'AUCTION_EVENT_TYPE_ORDER_CREATED' | 'AUCTION_EVENT_TYPE_PAYMENT_SUCCESS';

export const ROLE_CODE = {
  MERCHANT_OWNER: 'merchant_owner',
  ANCHOR: 'anchor',
  OPERATOR: 'operator',
  BUYER: 'buyer',
} as const;

export type RoleCode = (typeof ROLE_CODE)[keyof typeof ROLE_CODE];

export const PERMISSION_CODE = {
  TEAM_USER_CREATE: 'team.user.create',
  TEAM_USER_LIST: 'team.user.list',
  TEAM_USER_UPDATE_ROLE: 'team.user.update_role',
  TEAM_USER_UPDATE_STATUS: 'team.user.update_status',
  TEAM_USER_RESET_PASSWORD: 'team.user.reset_password',
  LOT_CREATE: 'lot.create',
  LOT_UPDATE: 'lot.update',
  LOT_QUEUE: 'lot.queue',
  LOT_VIEW_ADMIN: 'lot.view_admin',
  AUCTION_CONTROL: 'auction.control',
  ORDER_MANAGE: 'order.manage',
  REALTIME_VIEW: 'realtime.view',
  UPLOAD_IMAGE: 'upload.image',
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

export type UserStatus = ApiSchema<'UserStatus'>;
export const BACKOFFICE_ACCESS_PERMISSIONS: PermissionCode[] = [PERMISSION_CODE.LOT_VIEW_ADMIN, PERMISSION_CODE.AUCTION_CONTROL, PERMISSION_CODE.REALTIME_VIEW, PERMISSION_CODE.ORDER_MANAGE];

export function hasPermission(user: Pick<User, 'permissionCodes'> | undefined | null, permissionCode: PermissionCode | string) {
  return Boolean(user?.permissionCodes?.some((item) => item === permissionCode));
}

export function canAccessBackoffice(user?: Pick<User, 'permissionCodes' | 'status'> | null) {
  return Boolean(user && user.status === USER_STATUS.ACTIVE && BACKOFFICE_ACCESS_PERMISSIONS.some((permission) => hasPermission(user, permission)));
}

export type User = Normalized<'User', {
  id: string;
  username: string;
  nickname: string;
  roleCodes: RoleCode[];
  permissionCodes: PermissionCode[];
  mainAccountId: string;
  createdByUserId: string;
  status: UserStatus;
  createdAtUnixMs: number | string;
  updatedAtUnixMs: number | string;
}>;
export type AuthTokens = Normalized<'AuthTokens', { accessToken: string; refreshToken: string; accessExpiresAtUnixMs: number | string; refreshExpiresAtUnixMs: number | string }>;

export type BidRule = Normalized<'BidRule', {
  startPrice: Money;
  minIncrement: Money;
  capPrice?: Money;
  durationSeconds: number;
  antiSnipeWindowSeconds: number;
  antiSnipeExtendSeconds: number;
  maxExtendCount: number;
}>;
export type TrustRevealCard = Normalized<'TrustRevealCard', { id: string; lotId: string; type: TrustCardType; title: string; content: string; imageUrl?: string; revealed: boolean; revealedAtUnixMs: number | string }>;
export type Bid = Normalized<'Bid', { id: string; lotId: string; userId: string; nickname: string; amount: Money; createdAtUnixMs: number | string }>;
export type RankingItem = Normalized<'RankingItem', { rank: number; userId: string; nickname: string; amount: Money; bidAtUnixMs: number | string }>;
export type DuelState = Normalized<'DuelState', { active: boolean; lotId: string; userAId: string; userANickname: string; userBId: string; userBNickname: string; startedAtUnixMs: number | string; endsAtUnixMs: number | string; extendCount: number; maxExtendCount: number }>;
type LotStats = Normalized<'LotStats', { participantCount: number; bidCount: number }>;
export type Lot = Normalized<'Lot', { id: string; roomId: string; title: string; description: string; imageUrl: string; status: LotStatus; queueStatus?: LotQueueStatus; queuePosition?: number; rule: BidRule; currentPrice: Money; leadingUserId: string; leadingNickname: string; startedAtUnixMs: number | string; endsAtUnixMs: number | string; settledAtUnixMs: number | string; cancelledAtUnixMs?: number | string; winnerUserId: string; winnerNickname: string; finalPrice: Money; version: number | string; trustCards: TrustRevealCard[]; duelState: DuelState; playbookStage: PlaybookStage; stats: LotStats; cancelReason?: string; galleryImageUrls?: string[]; category?: string; tags?: string[]; estimatePrice?: Money; stock?: number | string; afterSaleNotes?: string; depositAmount?: Money }>;
export type RoomSnapshot = Normalized<'RoomSnapshot', { roomId: string; currentLot?: Lot; ranking: RankingItem[]; recentBids: Bid[]; playbookStage: PlaybookStage; serverTimeUnixMs: number | string }>;
export type RoomPresence = Normalized<'RoomPresence', { roomId: string; totalConnections: number | string; viewerConnections: number | string; operatorConnections: number | string; serverTimeUnixMs: number | string }>;
export type Room = Normalized<'AuctionRoom', { id: string; mainAccountId: string; name: string; platform: string; platformRoomId?: string; status: 'ACTIVE' | 'DISABLED' | string; createdByUserId?: string; createdAtUnixMs: number | string; updatedAtUnixMs: number | string }>;
export type AuctionEvent = Normalized<'AuctionEvent', { id: string; type: EventType; roomId: string; lotId: string; occurredAtUnixMs: number | string; lot?: Lot; bid?: Bid; ranking?: RankingItem[]; trustCard?: TrustRevealCard; duelState?: DuelState; snapshot?: RoomSnapshot; reason?: string; orderId?: string; paymentId?: string }>;
export type CreateLotRequest = Normalized<'CreateLotRequest', { roomId: string; title: string; description: string; imageUrl: string; rule: BidRule; trustCards: Omit<TrustRevealCard, 'lotId' | 'revealed' | 'revealedAtUnixMs'>[]; galleryImageUrls?: string[]; category?: string; tags?: string[]; estimatePrice?: Money; stock?: number; afterSaleNotes?: string; depositAmount?: Money }>;
export type PatchLotDraftRequest = Normalized<'PatchLotDraftRequest', Partial<CreateLotRequest> & { lotId: string }>;
type LotReplyFields = { lot?: Lot; event?: AuctionEvent; queuePosition?: number; trustCard?: TrustRevealCard; duelState?: DuelState; result?: ReplyResult };
export type CreateLotReply = Normalized<'LotReply', LotReplyFields>;
export type PatchLotDraftReply = Normalized<'LotReply', LotReplyFields>;
export type QueueLotReply = Normalized<'LotReply', LotReplyFields>;
export type StartLotReply = Normalized<'LotReply', LotReplyFields>;
export type RevealTrustCardReply = Normalized<'LotReply', LotReplyFields>;
export type StartDuelReply = Normalized<'LotReply', LotReplyFields>;
export type SettleLotReply = Normalized<'LotReply', LotReplyFields>;
export type CancelLotReply = Normalized<'LotReply', LotReplyFields>;
export type GetRoomSnapshotReply = Normalized<'GetRoomSnapshotReply', { snapshot?: RoomSnapshot; result?: ReplyResult }>;
export type ListRoomsReply = Normalized<'ListRoomsReply', { rooms?: Room[]; result?: ReplyResult }>;
export type AdminUserReply = Normalized<'UserReply', { user?: unknown; result?: ReplyResult }>;
export type AdminUsersReply = Normalized<'ListUsersReply', { users?: unknown[]; total?: number | string; page?: number; pageSize?: number; result?: ReplyResult }>;
export type AdminLotsReply = Normalized<'ListAdminLotPageReply', { lots?: unknown[]; total?: number | string; page?: number; pageSize?: number; result?: ReplyResult }>;
export type AdminOrdersReply = Normalized<'ListAuctionOrdersReply', { orders?: unknown[]; total?: number | string; page?: number; pageSize?: number; result?: ReplyResult }>;

export type UploadedAsset = { id: string; imageUrl: string; bucket: string; objectKey: string; mimeType: string; sizeBytes: number | string; status?: string; expiresAtUnixMs?: number | string };
export type UploadImageReply = {
  code?: number;
  message?: string;
  requestId?: string;
  serverTimeUnixMs?: number | string;
  data?: { asset?: UploadedAsset };
  result?: ReplyResult;
};

type AuthReplyFields = { user?: User; tokens?: AuthTokens; result?: ReplyResult };
export type LoginReply = Normalized<'AuthReply', AuthReplyFields>;
export type RegisterMerchantReply = Normalized<'AuthReply', AuthReplyFields>;
export type ResetPasswordReply = Normalized<'UserReply', { user?: User; result?: ReplyResult }>;
export type RefreshTokenReply = Normalized<'AuthReply', AuthReplyFields>;
export type LogoutReply = Normalized<'EmptyReply', { result?: ReplyResult }>;
export type GetMeReply = Normalized<'UserReply', { user?: User; result?: ReplyResult }>;
