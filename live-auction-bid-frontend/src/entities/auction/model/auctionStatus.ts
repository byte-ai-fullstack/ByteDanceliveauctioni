import type { Lot, LotStatus } from '../../../shared/api/types';
import type { StudioTone } from '../../../pages/host-console/components/studio-ui';
import type { OrderSummary } from '../../order/model/orderTypes';
import { isOrderClosedStatus, isOrderPaidStatus } from '../../order/model/orderStatus';

const ORDER_PAYMENT_WINDOW_MS = 30 * 60 * 1000;

export type AuctionUiStatus = '今日队列' | '草稿' | '准备中' | '待开拍' | '竞拍中' | '延时中' | '已落锤' | '已取消' | '异常' | '历史拍品';

const LOT_STATUS_FILTERS: Array<{ label: string; value: LotStatus | '' }> = [
  { label: '全部状态', value: '' },
  { label: '草稿', value: 'LOT_STATUS_DRAFT' },
  { label: '准备中', value: 'LOT_STATUS_READY' },
  { label: '待开拍', value: 'LOT_STATUS_QUEUED' },
  { label: '竞拍中', value: 'LOT_STATUS_LIVE' },
  { label: '延时中', value: 'LOT_STATUS_EXTENDED' },
  { label: '已落锤', value: 'LOT_STATUS_SETTLED' },
  { label: '已取消', value: 'LOT_STATUS_CANCELLED' },
  { label: '异常', value: 'LOT_STATUS_FAILED' },
];

export const CURRENT_LOT_STATUS_FILTERS = LOT_STATUS_FILTERS.filter((item) => (
  item.value !== 'LOT_STATUS_SETTLED'
  && item.value !== 'LOT_STATUS_CANCELLED'
  && item.value !== 'LOT_STATUS_FAILED'
));

export const HISTORY_LOT_STATUS_FILTERS: Array<{ label: string; value: LotStatus | '' }> = [
  { label: '全部历史', value: '' },
  { label: '已落锤', value: 'LOT_STATUS_SETTLED' },
  { label: '已取消', value: 'LOT_STATUS_CANCELLED' },
  { label: '异常', value: 'LOT_STATUS_FAILED' },
];

const lotStatusMeta: Record<string, { label: string; tone: StudioTone; ui: AuctionUiStatus }> = {
  LOT_STATUS_UNSPECIFIED: { label: '未知', tone: 'neutral', ui: '准备中' },
  LOT_STATUS_DRAFT: { label: '草稿', tone: 'info', ui: '草稿' },
  LOT_STATUS_READY: { label: '准备中', tone: 'info', ui: '准备中' },
  LOT_STATUS_QUEUED: { label: '待开拍', tone: 'warning', ui: '待开拍' },
  LOT_STATUS_LIVE: { label: '竞拍中', tone: 'success', ui: '竞拍中' },
  LOT_STATUS_EXTENDED: { label: '延时中', tone: 'warning', ui: '延时中' },
  LOT_STATUS_SETTLED: { label: '已落锤', tone: 'warning', ui: '已落锤' },
  LOT_STATUS_CANCELLED: { label: '已取消', tone: 'danger', ui: '已取消' },
  LOT_STATUS_FAILED: { label: '异常', tone: 'danger', ui: '异常' },
};

export function lotStatusLabel(status?: string) {
  return lotStatusMeta[status || '']?.label ?? status ?? '未知';
}

export function lotStatusTone(status?: string): StudioTone {
  return lotStatusMeta[status || '']?.tone ?? 'neutral';
}

export function uiStatusOfLot(lot: Pick<Lot, 'status' | 'playbookStage'>): AuctionUiStatus {
  if (lot.status === 'LOT_STATUS_QUEUED') return '待开拍';
  if (lot.status === 'LOT_STATUS_DRAFT' && lot.playbookStage === 'PLAYBOOK_STAGE_WARM_UP') return '草稿';
  return lotStatusMeta[lot.status]?.ui ?? '准备中';
}

export function isSettlementLot(lot: Pick<Lot, 'status' | 'settledAtUnixMs' | 'winnerUserId'>) {
  return lot.status === 'LOT_STATUS_SETTLED'
    || Number(lot.settledAtUnixMs || 0) > 0
    || Boolean(lot.winnerUserId);
}

export function isLiveLot(lot: Pick<Lot, 'status'>) {
  return lot.status === 'LOT_STATUS_LIVE' || lot.status === 'LOT_STATUS_EXTENDED';
}

export function canSettleLot(lot: Pick<Lot, 'leadingUserId'> | null | undefined) {
  return Boolean(lot?.leadingUserId);
}

export function isQueueReadyLot(lot: Pick<Lot, 'status' | 'queueStatus' | 'playbookStage'>) {
  return ['草稿', '准备中', '待开拍'].includes(uiStatusOfLot(lot));
}

export function isPreStartCancellableLot(lot: Pick<Lot, 'status'>) {
  if (['LOT_STATUS_LIVE', 'LOT_STATUS_EXTENDED', 'LOT_STATUS_SETTLED', 'LOT_STATUS_CANCELLED', 'LOT_STATUS_FAILED'].includes(lot.status)) return false;
  return lot.status === 'LOT_STATUS_DRAFT'
    || lot.status === 'LOT_STATUS_READY'
    || lot.status === 'LOT_STATUS_QUEUED';
}

function lotPaymentWindowPassed(lot: Pick<Lot, 'settledAtUnixMs'>, order?: Pick<OrderSummary, 'expiresAtUnixMs'> | null, nowMs = Date.now()) {
  const expiresAt = Number(order?.expiresAtUnixMs || 0);
  if (Number.isFinite(expiresAt) && expiresAt > 0) return expiresAt <= nowMs;
  const settledAt = Number(lot.settledAtUnixMs || 0);
  return Number.isFinite(settledAt) && settledAt > 0 && settledAt + ORDER_PAYMENT_WINDOW_MS <= nowMs;
}

export type SettlementOutcomeState = 'settling' | 'sold' | 'failed' | 'syncing';

export function settlementOutcomeDisplay(
  lot: Pick<Lot, 'status' | 'settledAtUnixMs' | 'winnerUserId'>,
  order?: Pick<OrderSummary, 'status' | 'paymentStatus' | 'expiresAtUnixMs'> | null,
  nowMs = Date.now(),
): { state: SettlementOutcomeState; label: string; tone: StudioTone; priceLabel: string; personLabel: string } {
  if (!isSettlementLot(lot)) {
    return { state: 'syncing', label: lotStatusLabel(lot.status), tone: lotStatusTone(lot.status), priceLabel: '当前价', personLabel: '领先用户' };
  }
  if (isOrderPaidStatus(order?.status, order?.paymentStatus)) {
    return { state: 'sold', label: '已成交', tone: 'success', priceLabel: '成交价', personLabel: '成交用户' };
  }
  if (isOrderClosedStatus(order?.status, order?.paymentStatus, order?.expiresAtUnixMs, nowMs) || lotPaymentWindowPassed(lot, order, nowMs)) {
    return { state: 'failed', label: '竞拍未成交', tone: 'danger', priceLabel: '落锤价', personLabel: '竞得者' };
  }
  return { state: 'settling', label: '截拍中', tone: 'warning', priceLabel: '落锤价', personLabel: '竞得者' };
}
