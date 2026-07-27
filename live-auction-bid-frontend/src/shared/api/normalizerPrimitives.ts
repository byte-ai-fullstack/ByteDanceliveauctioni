import type {
  EventType,
  LotQueueStatus,
  LotStatus,
  PermissionCode,
  PlaybookStage,
  RoleCode,
  TrustCardType,
  UserStatus,
} from './types';

type JsonRecord = Record<string, unknown>;

export const lotStatusValues = new Set<LotStatus>([
  'LOT_STATUS_UNSPECIFIED',
  'LOT_STATUS_DRAFT',
  'LOT_STATUS_READY',
  'LOT_STATUS_QUEUED',
  'LOT_STATUS_LIVE',
  'LOT_STATUS_EXTENDED',
  'LOT_STATUS_SETTLED',
  'LOT_STATUS_CANCELLED',
  'LOT_STATUS_FAILED',
]);

export const lotQueueStatusValues = new Set<LotQueueStatus>([
  'LOT_QUEUE_STATUS_UNSPECIFIED',
  'LOT_QUEUE_STATUS_NONE',
  'LOT_QUEUE_STATUS_QUEUED',
  'LOT_QUEUE_STATUS_NEXT',
]);

export const trustCardTypeValues = new Set<TrustCardType>([
  'TRUST_CARD_TYPE_UNSPECIFIED',
  'TRUST_CARD_TYPE_CERTIFICATE',
  'TRUST_CARD_TYPE_FLAW',
  'TRUST_CARD_TYPE_DETAIL',
  'TRUST_CARD_TYPE_SERVICE',
  'TRUST_CARD_TYPE_PRICE_REF',
]);

export const playbookStageValues = new Set<PlaybookStage>([
  'PLAYBOOK_STAGE_UNSPECIFIED',
  'PLAYBOOK_STAGE_WARM_UP',
  'PLAYBOOK_STAGE_TRUST_BLOCKED',
  'PLAYBOOK_STAGE_BIDDING_ACTIVE',
  'PLAYBOOK_STAGE_DUEL_READY',
  'PLAYBOOK_STAGE_DUEL_MODE',
  'PLAYBOOK_STAGE_SETTLE_READY',
]);

export const eventTypeValues = new Set<EventType>([
  'AUCTION_EVENT_TYPE_UNSPECIFIED',
  'AUCTION_EVENT_TYPE_ROOM_SNAPSHOT',
  'AUCTION_EVENT_TYPE_LOT_CREATED',
  'AUCTION_EVENT_TYPE_LOT_STARTED',
  'AUCTION_EVENT_TYPE_LOT_UPDATED',
  'AUCTION_EVENT_TYPE_BID_ACCEPTED',
  'AUCTION_EVENT_TYPE_BID_REJECTED',
  'AUCTION_EVENT_TYPE_RANKING_UPDATED',
  'AUCTION_EVENT_TYPE_TRUST_REVEALED',
  'AUCTION_EVENT_TYPE_DUEL_STARTED',
  'AUCTION_EVENT_TYPE_DUEL_ENDED',
  'AUCTION_EVENT_TYPE_LOT_SETTLED',
  'AUCTION_EVENT_TYPE_LOT_CANCELLED',
  'AUCTION_EVENT_TYPE_LOT_QUEUED',
  'AUCTION_EVENT_TYPE_BID_OUTBID',
  'AUCTION_EVENT_TYPE_AUCTION_EXTENDED',
  'AUCTION_EVENT_TYPE_AUCTION_CLOSED',
  'AUCTION_EVENT_TYPE_ORDER_CREATED',
  'AUCTION_EVENT_TYPE_PAYMENT_SUCCESS',
]);

export const roleCodeValues = new Set<RoleCode>([
  'merchant_owner',
  'anchor',
  'operator',
  'buyer',
]);

export const permissionCodeValues = new Set<PermissionCode>([
  'team.user.create',
  'team.user.list',
  'team.user.update_role',
  'team.user.update_status',
  'team.user.reset_password',
  'lot.create',
  'lot.update',
  'lot.queue',
  'lot.view_admin',
  'auction.control',
  'order.manage',
  'realtime.view',
  'upload.image',
  'bid.place',
  'order.pay',
  'order.view_own',
]);

export const userStatusValues = new Set<UserStatus>([
  'USER_STATUS_UNSPECIFIED',
  'USER_STATUS_ACTIVE',
  'USER_STATUS_DISABLED',
]);

export function asRecord(input: unknown, fieldName: string): JsonRecord {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error(`response missing ${fieldName}`);
  return input as JsonRecord;
}

export function field(raw: JsonRecord, name: string) {
  return raw[name];
}

export function requiredField<T>(value: T | undefined | null, name: string): T {
  if (value === undefined || value === null || value === '') throw new Error(`response missing ${name}`);
  return value;
}

export function optionalString(value: unknown) {
  return value === undefined || value === null ? undefined : String(value);
}

export function stringValue(value: unknown) {
  return value === undefined || value === null ? '' : String(value);
}

export function numberValue(value: unknown) {
  const number = Number(value ?? 0);
  return Number.isFinite(number) ? number : 0;
}

export function scalarValue(value: unknown, fallback: number | string = 0): number | string {
  if (value === undefined || value === null || value === '') return fallback;
  if (typeof value === 'number' || typeof value === 'string') return value;
  return String(value);
}

export function boolValue(value: unknown) {
  return Boolean(value);
}

export function arrayValue(value: unknown, name: string): unknown[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw new Error(`response ${name} must be an array`);
  return value;
}

export function normalizeStringArray(value: unknown, name: string) {
  return arrayValue(value, name).map((item) => String(item));
}

export function normalizeEnumArray<T extends string>(value: unknown, name: string, allowed: Set<T>): T[] {
  return normalizeStringArray(value, name).map((item) => normalizeEnum(item, name, allowed));
}

export function normalizeEnum<T extends string>(value: unknown, name: string, allowed: Set<T>, fallback?: T): T {
  if (value === undefined || value === null || value === '') {
    if (fallback !== undefined) return fallback;
    throw new Error(`response missing ${name}`);
  }
  const text = String(value);
  const normalized = text as T;
  if (!allowed.has(normalized)) throw new Error(`response ${name} has unknown enum value ${text}`);
  return normalized;
}
