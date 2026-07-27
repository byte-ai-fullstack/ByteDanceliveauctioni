import { useEffect, useRef, useState } from 'react';
import { canPayOrder, ownOrderForLot } from '../../../entities/order/model/privacy';
import { LOT_STATUS, type Lot, type OrderSummary } from '../../../shared/api/types';
import { formatMoney, moneyNumber } from '../../../shared/lib/money';
import { formatLeftMs, getLeftMsWithOffset } from '../../../shared/lib/time';
import { deriveLotDisplayState, lotHasBid, lotHasLockedResult, lotIsDisplayable, orderForLot, type LotDisplayState } from '../model/lotDisplayState';

type QueueLotView = {
  statusText: string;
  statusClass: string;
  priceLabel: string;
  priceValue: string;
  actionText: string;
  actionDisabled: boolean;
};

type BidPulse = {
  lotId: string;
  delta: number;
  total: number;
};

type BidPulseAccumulator = BidPulse & {
  expiresAt: number;
};

const BID_PULSE_ACCUMULATE_MS = 60000;
const BID_PULSE_VISIBLE_MS = 3000;
const DAY_MS = 24 * 60 * 60 * 1000;

const statusClassByState: Record<LotDisplayState, string> = {
  upcoming: 'isUpcoming',
  live: 'isLive',
  pendingPayment: 'isPendingPayment',
  finished: 'isFinished',
  failed: 'isFailed',
  cancelled: 'isCancelled',
  syncing: 'isUpcoming',
};

function lotResultPrice(lot: Lot) {
  if (lotHasLockedResult(lot) && moneyNumber(lot.finalPrice) > 0) return lot.finalPrice;
  if (moneyNumber(lot.currentPrice) > 0) return lot.currentPrice;
  return lot.rule.startPrice;
}

function queueLotView(lot: Lot, order: OrderSummary | null, paymentKnownPaid: boolean, nowMs?: number, currentLotId?: string): QueueLotView {
  const derivedState = deriveLotDisplayState(lot, { order, paymentKnownPaid, nowMs });
  const displayState = derivedState === 'live' && currentLotId && lot.id !== currentLotId ? 'syncing' : derivedState;
  const hasBid = lotHasBid(lot);
  const canPay = displayState === 'pendingPayment' && canPayOrder(order);
  const resultPrice = lotResultPrice(lot);
  const priceLabel = displayState === 'finished'
    ? '落槌价'
    : displayState === 'pendingPayment'
      ? '落槌价'
      : displayState === 'cancelled'
        ? hasBid ? '取消前价格' : '起拍价'
      : displayState === 'failed' && hasBid
        ? '落槌价'
        : displayState === 'upcoming' || displayState === 'syncing' || (displayState === 'failed' && !hasBid)
          ? '起拍价'
        : displayState === 'live'
          ? hasBid ? '当前最高价' : '起拍价'
          : hasBid
            ? '当前最高价'
        : '起拍价';
  const priceValue = displayState === 'finished' || displayState === 'pendingPayment' || hasBid
    ? formatMoney(resultPrice)
    : formatMoney(lot.rule.startPrice);
  const statusText = displayState === 'live'
    ? '竞拍中'
    : displayState === 'syncing'
      ? '状态同步中'
      : displayState === 'upcoming'
      ? '即将开拍'
      : displayState === 'pendingPayment'
        ? '截拍中'
        : displayState === 'cancelled'
          ? '已取消'
        : displayState === 'failed' && !hasBid
          ? '竞拍未成交'
          : '竞拍结束';
  return {
    statusText,
    statusClass: statusClassByState[displayState],
    priceLabel,
    priceValue,
    actionText: displayState === 'live' ? '去出价' : displayState === 'upcoming' || displayState === 'syncing' ? '去看看' : displayState === 'pendingPayment' ? canPay ? '去支付' : '截拍中' : displayState === 'cancelled' ? '已取消' : '已结束',
    actionDisabled: canPay ? false : displayState === 'finished' || displayState === 'failed' || displayState === 'cancelled',
  };
}

function ecomPriceParts(priceText: string) {
  const normalized = priceText.replace('元', '').replace(/[^\d.]/g, '');
  const [major = '0', minor = '00'] = normalized.split('.');

  return {
    major: major || '0',
    minor: (minor || '00').slice(0, 2).padEnd(2, '0')
  };
}

function ecomPriceSizeClass(major: string) {
  if (major.length >= 9) return 'amountNano';
  if (major.length >= 7) return 'amountTiny';
  if (major.length >= 6) return 'amountCompact';
  return '';
}

function ecomExplainText(view: QueueLotView) {
  if (view.statusClass === 'isLive') return '竞拍中';
  if (view.statusClass === 'isUpcoming') return '讲解中';
  return '';
}

function ecomCountText(count: number) {
  return count.toLocaleString('zh-CN');
}

function localDayStartMs(nowMs: number) {
  const date = new Date(nowMs);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
}

function nextLocalDayStartMs(nowMs: number) {
  const date = new Date(localDayStartMs(nowMs));
  date.setDate(date.getDate() + 1);
  return date.getTime();
}

function lotDisplayReferenceTime(lot: Lot, referenceNow: number) {
  const createdAtMs = Number(lot.createdAtUnixMs || 0);
  if (createdAtMs > 0) return createdAtMs;
  return referenceNow - DAY_MS;
}

function lotIsFromToday(lot: Lot, nowMs?: number) {
  const referenceNow = Number.isFinite(nowMs) ? Number(nowMs) : Date.now();
  const lotTime = lotDisplayReferenceTime(lot, referenceNow);
  if (lotTime <= 0) return true;
  return lotTime >= localDayStartMs(referenceNow) && lotTime < nextLocalDayStartMs(referenceNow);
}

function EcomCountdownTag({ endsAtUnixMs, nowMs }: { endsAtUnixMs?: number | string; nowMs?: number }) {
  const [leftMs, setLeftMs] = useState(0);

  useEffect(() => {
    const offsetMs = Number.isFinite(nowMs) ? Number(nowMs) - Date.now() : 0;
    const update = () => setLeftMs(getLeftMsWithOffset(endsAtUnixMs, offsetMs));
    const timer = window.setInterval(update, 500);
    update();
    return () => window.clearInterval(timer);
  }, [endsAtUnixMs, nowMs]);

  return <span className={`dyEcomCountdownTag${leftMs > 0 && leftMs < 10000 ? ' danger' : ''}`}>距离拍还剩 {formatLeftMs(leftMs)}</span>;
}

function lotSortScore(lot: Lot, order: OrderSummary | null, paymentKnownPaid: boolean, nowMs?: number, currentLotId?: string): number {
  const derivedState = deriveLotDisplayState(lot, { order, paymentKnownPaid, nowMs });
  const displayState = derivedState === 'live' && currentLotId && lot.id !== currentLotId ? 'syncing' : derivedState;
  if (displayState === 'live') return 0;
  if ((lot.status === LOT_STATUS.LIVE || lot.status === LOT_STATUS.EXTENDED) && (!currentLotId || lot.id === currentLotId)) return 0;
  if (displayState === 'upcoming') {
    if (lot.status === LOT_STATUS.QUEUED) return 2;
    return 3;
  }
  if (displayState === 'pendingPayment') return 4;
  if (displayState === 'finished') return 5;
  if (displayState === 'cancelled') return 6;
  if (displayState === 'failed') return 6;
  return 6;
}

export function LotQueueList({
  lots,
  currentLotId,
  selectedLotId,
  loading,
  error,
  onRefresh,
  onSelectLot,
  onPrimaryAction,
  onPayOrder,
  orders = [],
  paidLotIds = {},
  meId = '',
  nowMs,
}: {
  lots: Lot[];
  currentLotId?: string;
  selectedLotId?: string;
  loading: boolean;
  error: string;
  onRefresh: () => void;
  onSelectLot?: (lot: Lot) => void;
  onPrimaryAction?: (lot: Lot) => void;
  onPayOrder?: (order: OrderSummary) => void;
  orders?: OrderSummary[];
  paidLotIds?: Record<string, boolean>;
  meId?: string;
  nowMs?: number;
}) {
  const [bidPulse, setBidPulse] = useState<BidPulse | null>(null);
  const previousBidCountsRef = useRef<Record<string, number>>({});
  const bidPulseAccumulatorRef = useRef<BidPulseAccumulator | null>(null);
  const pendingBidPulseRef = useRef<BidPulse | null>(null);
  const showBidPulseTimerRef = useRef<number | null>(null);
  const hideBidPulseTimerRef = useRef<number | null>(null);
  const visibleLots = lots.filter((lot) => lotIsDisplayable(lot) && lotIsFromToday(lot, nowMs));
  const sortedLots = [...visibleLots].sort((a, b) => {
    const scoreDiff = lotSortScore(a, orderForLot(orders, a), Boolean(paidLotIds[a.id]), nowMs, currentLotId) - lotSortScore(b, orderForLot(orders, b), Boolean(paidLotIds[b.id]), nowMs, currentLotId);
    if (scoreDiff) return scoreDiff;
    return (a.queuePosition || 9999) - (b.queuePosition || 9999);
  });

  useEffect(() => {
    let nextPulse: BidPulse | null = null;
    const previousCounts = previousBidCountsRef.current;
    const nextCounts = { ...previousCounts };

    for (const lot of lots) {
      const count = Number(lot.stats?.bidCount || 0);
      const previousCount = previousCounts[lot.id];
      nextCounts[lot.id] = count;
      if (previousCount === undefined || count <= previousCount || lot.id !== currentLotId) continue;
      nextPulse = {
        lotId: lot.id,
        delta: count - previousCount,
        total: count,
      };
    }

    previousBidCountsRef.current = nextCounts;

    if (!nextPulse) return;
    const pulse = nextPulse;
    const now = Date.now();
    const currentAccumulator = bidPulseAccumulatorRef.current;
    const nextAccumulator = currentAccumulator?.lotId === pulse.lotId && currentAccumulator.expiresAt > now
      ? {
          lotId: pulse.lotId,
          delta: currentAccumulator.delta + pulse.delta,
          total: pulse.total,
          expiresAt: now + BID_PULSE_ACCUMULATE_MS,
        }
      : {
          ...pulse,
          expiresAt: now + BID_PULSE_ACCUMULATE_MS,
        };
    bidPulseAccumulatorRef.current = nextAccumulator;
    pendingBidPulseRef.current = {
      lotId: nextAccumulator.lotId,
      delta: nextAccumulator.delta,
      total: nextAccumulator.total,
    };

    if (showBidPulseTimerRef.current !== null) return;
    showBidPulseTimerRef.current = window.setTimeout(() => {
      const pendingPulse = pendingBidPulseRef.current;
      pendingBidPulseRef.current = null;
      showBidPulseTimerRef.current = null;
      if (!pendingPulse) return;

      setBidPulse((current) => (
        current?.lotId === pendingPulse.lotId
          ? { ...pendingPulse, delta: current.delta + pendingPulse.delta }
          : pendingPulse
      ));

      if (hideBidPulseTimerRef.current !== null) window.clearTimeout(hideBidPulseTimerRef.current);
      hideBidPulseTimerRef.current = window.setTimeout(() => {
        setBidPulse(null);
        hideBidPulseTimerRef.current = null;
      }, BID_PULSE_VISIBLE_MS);
    }, 0);
  }, [currentLotId, lots]);

  useEffect(() => () => {
    if (showBidPulseTimerRef.current !== null) window.clearTimeout(showBidPulseTimerRef.current);
    if (hideBidPulseTimerRef.current !== null) window.clearTimeout(hideBidPulseTimerRef.current);
  }, []);

  return (
    <section className="queuePanel">
      <header className="drawerSectionHeader">
        <div>
          <b>本场商品</b>
          <span>{visibleLots.length ? `主播今日上架 ${visibleLots.length} 件` : '今日暂无商品'}</span>
        </div>
        <button type="button" onClick={onRefresh} disabled={loading}>
          {loading ? '刷新中' : '刷新'}
        </button>
      </header>

      {error ? <p className="bidError" role="alert">{error}</p> : null}
      {!loading && !error && sortedLots.length === 0 ? <section className="drawerEmpty">今日暂无商品，等待主播从 PC 端上架</section> : null}

      <div className="queueList">
        {sortedLots.map((lot, index) => {
          const lotOrder = ownOrderForLot(orderForLot(orders, lot), meId, lot.id);
          const payableOrder = canPayOrder(lotOrder) ? lotOrder : null;
          const view = queueLotView(lot, lotOrder, Boolean(paidLotIds[lot.id]), nowMs, currentLotId);
          const active = lot.id === currentLotId;
          const selected = lot.id === selectedLotId;
          const priceParts = ecomPriceParts(view.priceValue);
          const explainText = active ? ecomExplainText(view) : '';
          const currentBidPulse = active && bidPulse?.lotId === lot.id ? bidPulse : null;
          return (
            <article
              className={`queueLot dyEcomLot ${view.statusClass} ${active ? 'active' : ''} ${selected ? 'selected' : ''}`}
              key={lot.id}
              role="button"
              tabIndex={0}
              onClick={() => onSelectLot?.(lot)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault();
                  onSelectLot?.(lot);
                }
              }}
            >
              <div className="queueLotMedia">
                {lot.imageUrl ? <img src={lot.imageUrl} alt={lot.title} /> : <div className="queueLotFallback">{index + 1}</div>}
                {currentBidPulse ? (
                  <div
                    className="dyEcomBidBadge"
                    key={`${currentBidPulse.lotId}-${currentBidPulse.delta}-${currentBidPulse.total}`}
                    aria-label={`累计出价${ecomCountText(currentBidPulse.total)}次`}
                  >
                    <svg className="dyEcomBidFire" viewBox="0 0 24 24" aria-hidden="true">
                      <path className="dyEcomBidFireGlow" d="M12 22c4.18 0 7.25-2.74 7.25-6.62 0-2.84-1.56-4.91-3.48-6.62-.49-.43-1.21-.08-1.21.57 0 .79-.27 1.48-.75 1.98-.36-2.48-1.66-4.62-4.07-6.84-.53-.49-1.4-.12-1.4.6 0 2.62-1.16 3.93-2.24 5.15-1.03 1.16-1.99 2.24-1.99 4.76C4.11 19.04 7.32 22 12 22Z" />
                      <path className="dyEcomBidFireCore" d="M12.17 20.02c2.04 0 3.6-1.34 3.6-3.29 0-1.49-.82-2.55-1.85-3.48-.34-.31-.86-.05-.86.41 0 .53-.18.98-.52 1.3-.28-1.35-1.05-2.52-2.41-3.76-.37-.34-.98-.08-.98.42 0 1.46-.65 2.19-1.25 2.87-.58.66-1.12 1.27-1.12 2.68 0 1.68 1.35 2.85 3.39 2.85Z" />
                    </svg>
                    <span className="dyEcomBidLabel">出价</span>
                    <strong>x{ecomCountText(currentBidPulse.total)}</strong>
                  </div>
                ) : null}
                {explainText ? (
                    <div className={`dyEcomExplain ${view.statusClass === 'isLive' ? 'isBidding' : ''}`}>
                      <i aria-hidden="true">
                        <b />
                        <b />
                        <b />
                      </i>
                      <span>{explainText}</span>
                    </div>
                ) : !active ? (
                  <span className="dyEcomRank">{lot.queuePosition || index + 1}</span>
                ) : null}
              </div>
                <div className="dyEcomLotInfo">
                  <div className="dyEcomTitle" data-e2e="promotion-title">
                    {lot.title || '未命名商品'}
                </div>
                <div className="dyEcomService">
                  <span className="dyEcomTag">{view.statusText}</span>
                  {view.statusClass === 'isLive' ? <EcomCountdownTag endsAtUnixMs={lot.endsAtUnixMs} nowMs={nowMs} /> : null}
                </div>
                <div className="dyEcomBottom">
                  <div className={`dyEcomPrice ${ecomPriceSizeClass(priceParts.major)}`} data-e2e="price-Area" aria-label={`${view.priceLabel}${view.priceValue}`}>
                    <span className="dyEcomPriceLabel">{view.priceLabel}</span>
                    <span className="dyEcomCurrency">¥</span>
                    <strong>{priceParts.major}</strong>
                    <span className="dyEcomDecimal">.{priceParts.minor}</span>
                  </div>
                  <button
                    type="button"
                    disabled={view.actionDisabled}
                    onClick={(event) => {
                      event.stopPropagation();
                      if (payableOrder && view.actionText === '去支付') {
                        onPayOrder?.(payableOrder);
                        return;
                      }
                      onPrimaryAction?.(lot);
                    }}
                  >
                    {view.actionText}
                  </button>
                </div>
              </div>
            </article>
          );
        })}
        {sortedLots.length > 0 ? <div className="dyEcomNoMore">已逛完所有直播间宝贝</div> : null}
      </div>
    </section>
  );
}
