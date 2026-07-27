import { apiRequest } from '../../../shared/api/httpClient';
import { normalizeLot } from '../../../shared/api/normalizers';
import { toQueryString } from '../../../shared/api/query';
import { assertOkResult } from '../../../shared/api/result';
import type { AdminOrdersReply } from '../../../shared/api/types';
import type { DeliveryAddressSnapshot, LotResultReply, OrderPage, OrderStatus, OrderSummary, PaymentStatus } from '../model/orderTypes';

export type AdminOrdersQuery = {
  page?: number;
  pageSize?: number;
  status?: OrderStatus | '';
  paymentStatus?: PaymentStatus | '';
  lotId?: string;
  buyer?: string;
};

function requiredValue<T>(value: T | undefined | null, field: string): T {
  if (value === undefined || value === null || value === '') throw new Error(`response missing ${field}`);
  return value;
}

function requireOrders(value: unknown[] | undefined): unknown[] {
  if (!Array.isArray(value)) throw new Error('response missing orders');
  return value;
}

function readField(raw: Record<string, unknown>, name: string) {
  return raw[name];
}

function asRecord(input: unknown, field: string): Record<string, unknown> {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error(`response missing ${field}`);
  return input as Record<string, unknown>;
}

function stringValue(value: unknown) {
  return value === undefined || value === null ? '' : String(value);
}

function normalizeDeliveryAddressSnapshot(input: unknown): DeliveryAddressSnapshot | null {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
  const raw = input as Record<string, unknown>;
  return {
    addressId: stringValue(readField(raw, 'addressId')),
    receiverName: stringValue(readField(raw, 'receiverName')),
    receiver: stringValue(readField(raw, 'receiver')),
    phone: stringValue(readField(raw, 'phone')),
    province: stringValue(readField(raw, 'province')),
    city: stringValue(readField(raw, 'city')),
    district: stringValue(readField(raw, 'district')),
    street: stringValue(readField(raw, 'street')),
    detail: stringValue(readField(raw, 'detail')),
    fullAddress: stringValue(readField(raw, 'fullAddress')),
  };
}

function normalizeOrderSummary(input: unknown): OrderSummary {
  const raw = asRecord(input, 'order');
  return {
    id: stringValue(requiredValue(readField(raw, 'id'), 'order.id')),
    lotId: stringValue(requiredValue(readField(raw, 'lotId'), 'order.lotId')),
    roomId: stringValue(requiredValue(readField(raw, 'roomId'), 'order.roomId')),
    lotTitle: stringValue(readField(raw, 'lotTitle')),
    lotImageUrl: stringValue(readField(raw, 'lotImageUrl')),
    buyerUserId: stringValue(readField(raw, 'buyerUserId')),
    buyerNickname: stringValue(readField(raw, 'buyerNickname')),
    status: stringValue(requiredValue(readField(raw, 'status'), 'order.status')) as OrderStatus,
    paymentStatus: stringValue(requiredValue(readField(raw, 'paymentStatus'), 'order.paymentStatus')) as PaymentStatus,
    paymentId: stringValue(readField(raw, 'paymentId')) || undefined,
    shippingAddressId: stringValue(readField(raw, 'shippingAddressId')) || undefined,
    shippingAddressSnapshot: normalizeDeliveryAddressSnapshot(readField(raw, 'shippingAddressSnapshot')),
    addressSnapshot: stringValue(readField(raw, 'addressSnapshot')) || undefined,
    amount: requiredValue(readField(raw, 'amount'), 'order.amount') as number | string,
    currency: stringValue(readField(raw, 'currency') ?? 'CNY') || 'CNY',
    createdAtUnixMs: requiredValue(readField(raw, 'createdAtUnixMs'), 'order.createdAtUnixMs') as number | string,
    updatedAtUnixMs: requiredValue(readField(raw, 'updatedAtUnixMs'), 'order.updatedAtUnixMs') as number | string,
    expiresAtUnixMs: requiredValue(readField(raw, 'expiresAtUnixMs'), 'order.expiresAtUnixMs') as number | string,
    paidAtUnixMs: readField(raw, 'paidAtUnixMs') as number | string | undefined,
    version: readField(raw, 'version') as number | string | undefined,
  };
}

function normalizeLotResultReply(input: unknown): LotResultReply {
  const raw = asRecord(input, 'lotResult');
  const lot = readField(raw, 'lot');
  const order = readField(raw, 'order');
  return {
    ...(raw as LotResultReply),
    lot: lot === undefined || lot === null ? undefined : normalizeLot(lot),
    auctionState: stringValue(readField(raw, 'auctionState')) as LotResultReply['auctionState'],
    order: order === undefined || order === null ? undefined : normalizeOrderSummary(order),
  };
}

export async function getLotResult(lotId: string, signal?: AbortSignal) {
  return normalizeLotResultReply(assertOkResult(await apiRequest<LotResultReply>({
    path: `/api/lots/${encodeURIComponent(lotId)}/result`,
    method: 'GET',
    operation: 'lot-result',
    signal,
  })));
}

export async function listAdminOrders(query: AdminOrdersQuery = {}, signal?: AbortSignal): Promise<OrderPage> {
  const page = query.page ?? 1;
  const pageSize = query.pageSize ?? 20;
  const reply = assertOkResult(await apiRequest<AdminOrdersReply>({
    path: `/api/admin/orders${toQueryString({
      page,
      pageSize,
      status: query.status,
      paymentStatus: query.paymentStatus,
      lotId: query.lotId?.trim(),
      buyer: query.buyer?.trim(),
    })}`,
    method: 'GET',
    operation: 'admin-list-orders',
    signal,
  }));
  return {
    orders: requireOrders(reply.orders).map(normalizeOrderSummary),
    total: Number(requiredValue(reply.total, 'total')),
    page: Number(requiredValue(reply.page, 'page')),
    pageSize: Number(requiredValue(reply.pageSize, 'pageSize')),
  };
}
