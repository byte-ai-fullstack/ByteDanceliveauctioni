import type { Lot, ReplyResult } from '../../../shared/api/types';
import type { components } from '../../../shared/api/generated/auction.schema';
import type { OrderStatus, PaymentStatus } from './orderStatus';

type ApiSchema<Name extends keyof components['schemas']> = components['schemas'][Name];
type Normalized<Name extends keyof components['schemas'], Fields extends object> = Omit<ApiSchema<Name>, keyof Fields> & Fields;

type AuctionState = ApiSchema<'AuctionState'> | (string & {});

export type DeliveryAddressSnapshot = Normalized<'AuctionDeliveryAddressSnapshot', {
  addressId?: string;
  receiverName?: string;
  receiver?: string;
  phone?: string;
  province?: string;
  city?: string;
  district?: string;
  street?: string;
  detail?: string;
  fullAddress?: string;
}>;

export type OrderSummary = Normalized<'AuctionOrderSummary', {
  id: string;
  lotId: string;
  roomId: string;
  lotTitle: string;
  lotImageUrl: string;
  buyerUserId: string;
  buyerNickname?: string;
  status: OrderStatus;
  paymentStatus: PaymentStatus;
  paymentId?: string;
  shippingAddressId?: string;
  shippingAddressSnapshot?: DeliveryAddressSnapshot | null;
  addressSnapshot?: string;
  amount: number | string;
  currency: string;
  createdAtUnixMs: number | string;
  updatedAtUnixMs: number | string;
  expiresAtUnixMs: number | string;
  paidAtUnixMs?: number | string;
  version?: number | string;
}>;

export type LotResultReply = Normalized<'GetLotResultReply', {
  result?: ReplyResult;
  lot?: Lot;
  auctionState?: AuctionState;
  order?: OrderSummary;
}>;

export type OrderPage = {
  orders: OrderSummary[];
  total: number;
  page: number;
  pageSize: number;
};
