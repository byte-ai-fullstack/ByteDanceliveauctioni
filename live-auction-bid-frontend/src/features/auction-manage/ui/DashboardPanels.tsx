import type { CSSProperties } from 'react';
import { CheckCircle2, Clock3, Gavel, ListChecks, Package, ReceiptText, ShieldAlert, ShoppingBag } from 'lucide-react';
import { lotStatusLabel, lotStatusTone } from '../../../entities/auction/model/auctionStatus';
import { paymentStatusLabel, paymentStatusTone } from '../../../entities/order/model/orderStatus';
import type { Bid, Lot } from '../../../shared/api/types';
import { formatMoneyText } from '../../../shared/lib/format';
import { AppLink } from '../../../shared/router/AppLink';
import { StudioBadge, StudioCard, StudioEmptyState, type StudioTone } from '../../../pages/host-console/components/studio-ui';
import { moneyCents, moneyYuan, type DashboardAnalytics, type LotPerformance } from '../model/dashboardAnalytics';
import { createRiskGradient, formatCountdown, formatPercent, formatYuan } from './dashboardFormat';
import { useSecondClock } from './useSecondClock';

export function LiveBusinessPanel({ currentLot, recentBids, serverTimeUnixMs, snapshotReceivedAt, participantCount }: { currentLot: Lot | null; recentBids: Bid[]; serverTimeUnixMs: number; snapshotReceivedAt: number; participantCount: number }) {
  return <StudioCard title="本场焦点" subtitle="Live business" className="merchantPanel merchantLivePanel" actions={currentLot ? <StudioBadge tone={lotStatusTone(currentLot.status)}>{lotStatusLabel(currentLot.status)}</StudioBadge> : <StudioBadge tone="neutral">待开拍</StudioBadge>}>
    {currentLot ? <div className="merchantLiveLot">
      <div className="merchantLiveImage">{currentLot.imageUrl ? <img src={currentLot.imageUrl} alt={currentLot.title} /> : <Gavel size={34} />}</div>
      <div className="merchantLiveBody">
        <h3>{currentLot.title}</h3>
        <div className="merchantLivePrice"><span>当前价</span><b>{formatMoneyText(currentLot.currentPrice)}</b></div>
        <div className="merchantLiveFacts">
          <span>剩余时间：<LiveCountdown endsAtUnixMs={Number(currentLot.endsAtUnixMs || 0)} serverTimeUnixMs={serverTimeUnixMs} snapshotReceivedAt={snapshotReceivedAt} /></span>
          <span>领先用户：<b>{currentLot.leadingNickname || currentLot.leadingUserId || '暂无'}</b></span>
          <span>参拍人数：<b>{participantCount.toLocaleString('zh-CN')}</b></span>
        </div>
        <div className="merchantBidTape" aria-label="最近出价">
          {recentBids.length ? recentBids.map((bid) => <div key={bid.id}><span>{bid.nickname || bid.userId || '买家'}</span><b>{formatMoneyText(bid.amount)}</b></div>) : <div><span>暂无出价</span><b>等待开拍</b></div>}
        </div>
      </div>
    </div> : <StudioEmptyState compact icon={<Gavel size={28} />} title="当前没有正在拍" description="开拍后这里展示当前价、领先用户和最近出价。" action={<AppLink className="studioButton studioButton-primary studioButton-sm" to="/admin/auctions">进入本场队列</AppLink>} />}
  </StudioCard>;
}

function LiveCountdown({ endsAtUnixMs, serverTimeUnixMs, snapshotReceivedAt }: { endsAtUnixMs: number; serverTimeUnixMs: number; snapshotReceivedAt: number }) {
  const nowMs = useSecondClock();
  const serverNowMs = serverTimeUnixMs
    ? serverTimeUnixMs + Math.max(0, nowMs - snapshotReceivedAt)
    : nowMs;
  return <b>{formatCountdown(endsAtUnixMs - serverNowMs)}</b>;
}

export function ActionChecklist({ analytics }: { analytics: DashboardAnalytics }) {
  const actions = [
    {
      label: '催付',
      href: '/admin/orders',
      count: analytics.pendingOrders.length,
      detail: `${formatYuan(analytics.pendingAmountYuan)} 待支付`,
      icon: <Clock3 size={18} />,
      tone: 'warning' as StudioTone,
    },
    {
      label: '补队列',
      href: '/admin/auctions/create',
      count: Math.max(0, 3 - analytics.queuedLots.length),
      detail: analytics.queuedLots.length >= 3 ? '本场队列充足' : `待开拍仅 ${analytics.queuedLots.length} 件`,
      icon: <Package size={18} />,
      tone: analytics.queuedLots.length >= 3 ? 'success' as StudioTone : 'warning' as StudioTone,
    },
    {
      label: '处理异常',
      href: '/admin/orders',
      count: analytics.abnormalOrders.length + analytics.abnormalLots.length,
      detail: `${analytics.abnormalOrders.length} 个订单，${analytics.abnormalLots.length} 件拍品`,
      icon: <ShieldAlert size={18} />,
      tone: 'danger' as StudioTone,
    },
    {
      label: '复盘低转化拍品',
      href: '/admin/auctions/history',
      count: analytics.lowConversionLots.length,
      detail: '低溢价、流拍或付款超时',
      icon: <ListChecks size={18} />,
      tone: 'info' as StudioTone,
    },
  ];
  return <StudioCard title="行动清单" subtitle="Next actions" className="merchantPanel merchantActionPanel">
    <div className="merchantActionList">{actions.map((action) => <AppLink key={action.label} to={action.href} className={`merchantActionItem merchantAction-${action.tone}`}>
      <span>{action.icon}</span>
      <div><b>{action.label}</b><small>{action.detail}</small></div>
      <strong>{action.count.toLocaleString('zh-CN')}</strong>
    </AppLink>)}</div>
  </StudioCard>;
}

export function OrderRiskPanel({ analytics }: { analytics: DashboardAnalytics }) {
  const nowMs = useSecondClock();
  const total = analytics.statusDistribution.reduce((sum, item) => sum + item.value, 0);
  const pending = analytics.pendingOrders
    .slice()
    .sort((a, b) => (Number(a.expiresAtUnixMs || 0) - Number(b.expiresAtUnixMs || 0)) || (moneyCents(b.amount) - moneyCents(a.amount)))
    .slice(0, 5);
  const gradient = createRiskGradient(analytics.statusDistribution);
  return <div className="merchantRiskPanelBody">
    {total ? <div className="merchantRiskDonut" key={`${total}-${gradient}`} style={{ '--risk-gradient': gradient } as CSSProperties} aria-label={`订单状态分布，共 ${total} 个订单`}><strong>{total.toLocaleString('zh-CN')}</strong><span>订单分布</span></div> : <StudioEmptyState compact icon={<ReceiptText size={28} />} title="暂无订单分布" description="当前范围没有订单。" />}
    <div className="merchantRiskLegend">{analytics.statusDistribution.map((item) => <div key={item.label}><StudioBadge tone={item.tone}>{item.label}</StudioBadge><b>{item.value.toLocaleString('zh-CN')}</b></div>)}</div>
    <div className="merchantCountdownList">
      <h3>待支付倒计时</h3>
      {pending.length ? pending.map((order) => {
        const leftMs = Number(order.expiresAtUnixMs || 0) - nowMs;
        return <AppLink to="/admin/orders" key={order.id} className={leftMs <= 0 ? 'expired' : ''}>
          <div><b>{order.lotTitle || '落锤订单'}</b><span>{order.buyerNickname || order.buyerUserId || '买家'} · {formatYuan(moneyYuan(order.amount))}</span></div>
          <strong>{leftMs <= 0 ? '已超时' : formatCountdown(leftMs)}</strong>
        </AppLink>;
      }) : <StudioEmptyState compact icon={<CheckCircle2 size={22} />} title="暂无待支付订单" description="当前范围没有需要催付的订单。" />}
    </div>
  </div>;
}

export function LotPerformanceTable({ lots, refreshSeq, onOpenLot }: { lots: LotPerformance[]; refreshSeq: number; onOpenLot: (lot: Lot) => void }) {
  if (!lots.length) return <StudioEmptyState compact icon={<ShoppingBag size={28} />} title="暂无拍品明细" description="当前范围没有拍品表现数据。" />;
  return <div className="merchantLotTable" key={refreshSeq}>
    <div className="merchantLotTableHead">
      <span>拍品</span>
      <span>状态</span>
      <span>起拍价</span>
      <span>结果金额</span>
      <span>溢价率</span>
      <span>出价次数</span>
      <span>支付</span>
      <span>操作</span>
    </div>
    {lots.slice(0, 12).map((item) => <div className="merchantLotRow" key={item.lot.id}>
      <div className="merchantLotIdentity" data-label="拍品">
        <span>{item.lot.imageUrl ? <img src={item.lot.imageUrl} alt={item.lot.title} /> : <ShoppingBag size={20} />}</span>
        <div><b>{item.lot.title}</b><small>{item.lot.category || '未分类'}</small></div>
      </div>
      <div data-label="状态"><StudioBadge tone={item.statusTone}>{item.statusLabel}</StudioBadge></div>
      <div data-label="起拍价">{formatYuan(item.startYuan)}</div>
      <div data-label={item.amountLabel}><strong>{item.amountYuan > 0 ? formatYuan(item.amountYuan) : '未成交'}</strong></div>
      <div data-label="溢价率">{formatPercent(item.premiumRate)}</div>
      <div data-label="出价次数">{item.bidCount === undefined ? '—' : item.bidCount.toLocaleString('zh-CN')}</div>
      <div data-label="支付">{item.paymentStatus ? <StudioBadge tone={paymentStatusTone(item.paymentStatus)}>{paymentStatusLabel(item.paymentStatus)}</StudioBadge> : <StudioBadge tone="neutral">无订单</StudioBadge>}</div>
      <div data-label="操作"><button type="button" className="merchantLotLinkButton" onClick={() => onOpenLot(item.lot)}>查看</button></div>
    </div>)}
  </div>;
}
