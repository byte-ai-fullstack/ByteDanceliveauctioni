import type { OrderSummary } from '../../order/model/orderTypes';
import { isSettlementLot, lotStatusLabel, lotStatusTone, settlementOutcomeDisplay } from '../../../entities/auction/model/auctionStatus';
import { isAbnormalOrder, type PaymentStatus } from '../../../entities/order/model/orderStatus';
import type { Lot, Money, RoomSnapshot } from '../../../shared/api/types';
import type { StudioTone } from '../../../pages/host-console/components/studio-ui';

export type DashboardRange = 'today' | 'live' | '7d' | '30d';

export type TimeBucket = {
  label: string;
  start: number;
  end: number;
  gmv: number;
  paid: number;
  pending: number;
  abnormal: number;
  orders: number;
};

export type FunnelStep = {
  label: string;
  value: number;
  hint: string;
};

export type LotPerformance = {
  lot: Lot;
  amountYuan: number;
  amountLabel: string;
  startYuan: number;
  premiumRate: number;
  participantCount: number;
  bidCount?: number;
  paymentStatus?: PaymentStatus;
  paid: boolean;
  statusLabel: string;
  statusTone: StudioTone;
};

export type DashboardAnalytics = {
  rangeLots: Lot[];
  rangeOrders: OrderSummary[];
  paidOrders: OrderSummary[];
  pendingOrders: OrderSummary[];
  abnormalOrders: OrderSummary[];
  paidAmountYuan: number;
  pendingAmountYuan: number;
  abnormalAmountYuan: number;
  gmvYuan: number;
  paymentRate: number;
  dealRate: number;
  participantCount: number;
  averageDealYuan: number;
  queuedLots: Lot[];
  abnormalLots: Lot[];
  lowConversionLots: Lot[];
  timeSeries: TimeBucket[];
  funnel: FunnelStep[];
  topLots: LotPerformance[];
  lotPerformance: LotPerformance[];
  statusDistribution: Array<{ label: string; value: number; tone: StudioTone }>;
};

const DAY_MS = 24 * 60 * 60 * 1000;

export function buildDashboardAnalytics({ lots, orders, snapshot, range, nowMs }: { lots: Lot[]; orders: OrderSummary[]; snapshot: RoomSnapshot | null; range: DashboardRange; nowMs: number }): DashboardAnalytics {
  const startMs = getRangeStartMs(range, nowMs);
  const rangeOrders = filterOrdersByRange(orders, range, startMs);
  const rangeLots = filterLotsByRange(lots, range, startMs);
  const paidOrders = rangeOrders.filter(isPaidOrder);
  const pendingOrders = rangeOrders.filter(isPendingOrder);
  const abnormalOrders = rangeOrders.filter((order) => isAbnormalOrder(order.status, order.paymentStatus));
  const paidAmountYuan = sumOrderYuan(paidOrders);
  const pendingAmountYuan = sumOrderYuan(pendingOrders);
  const abnormalAmountYuan = sumOrderYuan(abnormalOrders);
  const gmvYuan = paidAmountYuan;
  const settledLots = rangeLots.filter(isSettlementLot);
  const startedLots = rangeLots.filter(hasStartedLot);
  const withBidLots = rangeLots.filter((lot) => lotHasBid(lot, snapshot, rangeOrders));
  const paidLotIds = new Set(paidOrders.map((order) => order.lotId).filter(Boolean));
  const queuedLots = lots.filter(isQueueLot);
  const abnormalLots = lots.filter(isAbnormalLot);
  const participantCount = rangeLots.reduce((sum, lot) => sum + (lot.stats?.participantCount ?? 0), 0);
  const lotPerformance = buildLotPerformance(rangeLots, rangeOrders, nowMs).sort((a, b) => b.amountYuan - a.amountYuan);
  const lowConversionLots = lotPerformance.filter((item) => item.lot.status === 'LOT_STATUS_FAILED' || item.premiumRate <= 0 || (!item.paid && isSettlementLot(item.lot)));
  const timeSeries = buildTimeSeries(range, rangeOrders, nowMs);

  return {
    rangeLots,
    rangeOrders,
    paidOrders,
    pendingOrders,
    abnormalOrders,
    paidAmountYuan,
    pendingAmountYuan,
    abnormalAmountYuan,
    gmvYuan,
    paymentRate: rangeOrders.length ? paidOrders.length / rangeOrders.length * 100 : 0,
    dealRate: startedLots.length ? paidLotIds.size / startedLots.length * 100 : 0,
    participantCount,
    averageDealYuan: paidOrders.length ? paidAmountYuan / paidOrders.length : 0,
    queuedLots,
    abnormalLots,
    lowConversionLots: lowConversionLots.map((item) => item.lot),
    timeSeries,
    funnel: [
      { label: '已创建拍品', value: rangeLots.length, hint: '当前范围内可统计拍品' },
      { label: '已上架', value: rangeLots.filter(isListedLot).length, hint: '已进入上架或队列' },
      { label: '已开拍', value: startedLots.length, hint: '产生开拍时间' },
      { label: '有出价', value: withBidLots.length, hint: '有领先用户或最近出价' },
      { label: '已落锤', value: settledLots.length, hint: '已进入结算的拍品' },
      { label: '已成交', value: paidLotIds.size, hint: '支付成功订单关联拍品' },
    ],
    topLots: lotPerformance.filter((item) => item.amountYuan > 0).slice(0, 10),
    lotPerformance,
    statusDistribution: [
      { label: '已支付', value: paidOrders.length, tone: 'success' },
      { label: '待支付', value: pendingOrders.length, tone: 'warning' },
      { label: '超时', value: rangeOrders.filter((order) => order.status === 'EXPIRED').length, tone: 'danger' },
      { label: '异常', value: abnormalOrders.length, tone: 'danger' },
    ],
  };
}

function buildLotPerformance(lots: Lot[], orders: OrderSummary[], nowMs: number): LotPerformance[] {
  const ordersByLot = new Map<string, OrderSummary[]>();
  orders.forEach((order) => {
    ordersByLot.set(order.lotId, [...(ordersByLot.get(order.lotId) ?? []), order]);
  });
  return lots.map((lot) => {
    const relatedOrders = ordersByLot.get(lot.id) ?? [];
    const paidOrder = relatedOrders.find(isPaidOrder);
    const latestOrder = paidOrder ?? relatedOrders[0];
    const outcome = isSettlementLot(lot) ? settlementOutcomeDisplay(lot, latestOrder, nowMs) : null;
    const amountYuan = latestOrder ? moneyYuan(latestOrder.amount) : moneyYuan(lotResultMoney(lot));
    const startYuan = moneyYuan(lot.rule.startPrice);
    return {
      lot,
      amountYuan,
      amountLabel: outcome?.priceLabel ?? (amountYuan > 0 ? '当前价' : '起拍价'),
      startYuan,
      premiumRate: startYuan > 0 ? (amountYuan - startYuan) / startYuan * 100 : 0,
      participantCount: lot.stats?.participantCount ?? (latestOrder ? 1 : 0),
      bidCount: lot.stats?.bidCount ?? 0,
      paymentStatus: latestOrder?.paymentStatus,
      paid: latestOrder ? isPaidOrder(latestOrder) : false,
      statusLabel: outcome?.label ?? lotStatusLabel(lot.status),
      statusTone: outcome?.tone ?? lotStatusTone(lot.status),
    };
  });
}

function buildTimeSeries(range: DashboardRange, orders: OrderSummary[], nowMs: number): TimeBucket[] {
  const count = range === 'today' ? 12 : range === '7d' ? 7 : range === '30d' ? 10 : 8;
  const startMs = range === 'live'
    ? Math.min(...orders.map((order) => orderTime(order)).filter(Boolean), nowMs - 4 * 60 * 60 * 1000)
    : getRangeStartMs(range, nowMs);
  const span = Math.max(1, nowMs - startMs);
  const bucketSize = Math.max(1, span / count);
  const buckets = Array.from({ length: count }, (_, index) => {
    const start = startMs + bucketSize * index;
    const end = index === count - 1 ? nowMs + 1 : start + bucketSize;
    return { label: formatBucketLabel(start, range), start, end, gmv: 0, paid: 0, pending: 0, abnormal: 0, orders: 0 } satisfies TimeBucket;
  });
  orders.forEach((order) => {
    const time = orderTime(order);
    if (!time) return;
    const bucket = buckets.find((item) => time >= item.start && time < item.end);
    if (!bucket) return;
    const amount = moneyYuan(order.amount);
    bucket.orders += 1;
    if (isPaidOrder(order)) {
      bucket.gmv += amount;
      bucket.paid += amount;
    } else if (isPendingOrder(order)) {
      bucket.pending += amount;
    }
    if (isAbnormalOrder(order.status, order.paymentStatus)) bucket.abnormal += amount;
  });
  return buckets;
}

function lotResultMoney(lot: Lot) {
  if (isSettlementLot(lot) && moneyCents(lot.finalPrice) > 0) return lot.finalPrice;
  if (moneyCents(lot.currentPrice) > 0) return lot.currentPrice;
  return lot.rule.startPrice;
}

function filterOrdersByRange(orders: OrderSummary[], range: DashboardRange, startMs: number) {
  if (range === 'live') return orders;
  return orders.filter((order) => orderTime(order) >= startMs);
}

function filterLotsByRange(lots: Lot[], range: DashboardRange, startMs: number) {
  if (range === 'live') return lots;
  return lots.filter((lot) => {
    const time = lotBusinessTime(lot);
    if (time) return time >= startMs;
    return range === '30d';
  });
}

function getRangeStartMs(range: DashboardRange, nowMs: number) {
  if (range === 'today') {
    const start = new Date(nowMs);
    start.setHours(0, 0, 0, 0);
    return start.getTime();
  }
  if (range === '7d') return nowMs - 7 * DAY_MS;
  if (range === '30d') return nowMs - 30 * DAY_MS;
  return 0;
}

function isPaidOrder(order: OrderSummary) {
  return order.status === 'PAID' || order.paymentStatus === 'SUCCESS';
}

function isPendingOrder(order: OrderSummary) {
  if (isAbnormalOrder(order.status, order.paymentStatus) || isPaidOrder(order)) return false;
  return order.status === 'CREATED' || order.status === 'PENDING_PAYMENT' || order.paymentStatus === 'INIT' || order.paymentStatus === 'PROCESSING';
}

function hasStartedLot(lot: Lot) {
  return Number(lot.startedAtUnixMs || 0) > 0 || ['LOT_STATUS_LIVE', 'LOT_STATUS_EXTENDED', 'LOT_STATUS_SETTLED', 'LOT_STATUS_FAILED'].includes(lot.status);
}

function isListedLot(lot: Lot) {
  return !['LOT_STATUS_UNSPECIFIED', 'LOT_STATUS_DRAFT'].includes(lot.status);
}

function isQueueLot(lot: Lot) {
  return lot.status === 'LOT_STATUS_QUEUED' || lot.status === 'LOT_STATUS_READY';
}

function isAbnormalLot(lot: Lot) {
  return lot.status === 'LOT_STATUS_CANCELLED' || lot.status === 'LOT_STATUS_FAILED';
}

function lotHasBid(lot: Lot, snapshot: RoomSnapshot | null, orders: OrderSummary[]) {
  if ((lot.stats?.bidCount ?? 0) > 0) return true;
  if (lot.leadingUserId || lot.winnerUserId) return true;
  if (moneyCents(lot.currentPrice) > moneyCents(lot.rule.startPrice)) return true;
  if (snapshot?.recentBids.some((bid) => bid.lotId === lot.id)) return true;
  return orders.some((order) => order.lotId === lot.id);
}

function lotBusinessTime(lot: Lot) {
  return Math.max(
    Number(lot.settledAtUnixMs || 0),
    Number(lot.cancelledAtUnixMs || 0),
    Number(lot.startedAtUnixMs || 0),
    Number(lot.endsAtUnixMs || 0),
  );
}

function orderTime(order: OrderSummary) {
  return Number(order.createdAtUnixMs || order.paidAtUnixMs || order.updatedAtUnixMs || 0);
}

function sumOrderYuan(orders: OrderSummary[]) {
  return orders.reduce((sum, order) => sum + moneyYuan(order.amount), 0);
}

export function moneyCents(value?: Money | number | string | null) {
  if (typeof value === 'object' && value !== null && 'amount' in value) return Number(value.amount || 0);
  return Number(value || 0);
}

export function moneyYuan(value?: Money | number | string | null) {
  return moneyCents(value) / 100;
}

function formatBucketLabel(timeMs: number, range: DashboardRange) {
  const date = new Date(timeMs);
  if (range === 'today' || range === 'live') return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
  return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' });
}
