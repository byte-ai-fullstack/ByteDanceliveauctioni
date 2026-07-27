import { useEffect, useMemo } from 'react';
import { AlertTriangle, ChevronLeft, ChevronRight, CircleDollarSign, Clock3, Package, ReceiptText, RefreshCw, Search, ShieldAlert, X } from 'lucide-react';
import type { AdminOrdersQuery } from '../order/api/orderApi';
import type { DeliveryAddressSnapshot, LotResultReply, OrderSummary } from '../../entities/order/model/orderTypes';
import { ORDER_STATUS_FILTERS, isAbnormalOrder, isOrderPaidStatus, orderStatusLabel, orderStatusTone, paymentStatusLabel } from '../../entities/order/model/orderStatus';
import { formatAmountText, formatDateTimeText } from '../../shared/lib/format';
import { StudioBadge, StudioButton, StudioCard, StudioEmptyState, StudioErrorState, StudioField, StudioMetricCard, StudioPageHeader, StudioTableSkeleton, StudioToastViewport } from '../../pages/host-console/components/studio-ui';
import { useStudioToast } from '../../pages/host-console/components/studio-toast';
import { useOrderManagementPage } from './model/useOrderManagementPage';

const DEFAULT_PAGE_SIZE = 20;

export function OrderManagementPage() {
  const { toasts } = useStudioToast();
  const { query, orders, total, loading, error, detail, detailLoading, closeDetail, totalPages, currentPage, goPrevPage, goNextPage, syncOrders, updateQuery, openDetail } = useOrderManagementPage(DEFAULT_PAGE_SIZE);

  const metrics = useMemo(() => {
    const pending = orders.filter((order) => order.status === 'PENDING_PAYMENT').length;
    const paid = orders.filter((order) => order.status === 'PAID' || order.paymentStatus === 'SUCCESS').length;
    const abnormal = orders.filter((order) => isAbnormalOrder(order.status, order.paymentStatus)).length;
    return { pending, paid, abnormal };
  }, [orders]);

  return <section className="postLivePage orderReviewPage">
    <StudioToastViewport toasts={toasts} />
    <StudioCard padding="lg" className="orderReviewHero">
      <StudioPageHeader
        eyebrow="Admin · Orders"
        title="成交处理"
        description="管理本场竞拍产生的全部落锤订单，跟踪支付状态与履约进度。订单数据来自后台 /api/admin/orders 接口，成交详情通过 /api/lots/{lotId}/result 获取。"
        actions={<StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} loading={loading} onClick={() => void syncOrders()}>{loading ? '同步中' : '刷新订单'}</StudioButton>}
      />
    </StudioCard>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    <section className="postLiveMetricGrid">
      <StudioMetricCard icon={<ReceiptText />} label="订单总数" value={total.toLocaleString('zh-CN')} trend="本场全部落锤订单" tone="info" />
      <StudioMetricCard icon={<Clock3 />} label="待支付" value={metrics.pending.toLocaleString('zh-CN')} trend="等待买家完成支付" tone="warning" />
      <StudioMetricCard icon={<CircleDollarSign />} label="已支付" value={metrics.paid.toLocaleString('zh-CN')} trend="支付成功，进入履约" tone="success" />
      <StudioMetricCard icon={<ShieldAlert />} label="异常订单" value={metrics.abnormal.toLocaleString('zh-CN')} trend="取消 / 过期 / 退款 / 支付失败" tone="danger" />
    </section>
    <StudioCard padding="md" className="orderReviewFilters">
      <div className="orderFilterBar" aria-label="订单筛选">
        <label className="orderFilterSearch"><Search size={15} /><input value={query.buyer || ''} onChange={(e) => updateQuery({ buyer: e.target.value })} onKeyDown={(e) => { if (e.key === 'Enter') void syncOrders({ ...query, page: 1 }); }} placeholder="搜索买家 ID / 昵称" /></label>
        <span className="orderFilterSep" aria-hidden="true" />
        <StudioField label="订单状态"><select value={query.status || ''} onChange={(e) => updateQuery({ status: e.target.value as AdminOrdersQuery['status'] })}>{ORDER_STATUS_FILTERS.map((item) => <option key={item.label} value={item.value}>{item.label}</option>)}</select></StudioField>
        <StudioField label="竞拍 ID"><input value={query.lotId || ''} onChange={(e) => updateQuery({ lotId: e.target.value })} placeholder="输入 lotId" /></StudioField>
        <StudioButton type="button" variant="primary" icon={<Search size={15} />} onClick={() => void syncOrders({ ...query, page: 1 })}>查询</StudioButton>
      </div>
    </StudioCard>
    {loading ? <StudioTableSkeleton rows={6} columns={7} /> : error && !orders.length ? <StudioErrorState icon={<AlertTriangle size={34} />} title="订单列表加载失败" description={error} action={<StudioButton type="button" variant="secondary" onClick={() => void syncOrders()}>重试</StudioButton>} /> : <section className="auctionHistoryListWrap orderReviewListWrap">
      <div className="historyListHeader">
        <strong>共 {total} 条订单 · 每页 {DEFAULT_PAGE_SIZE} 条</strong>
        <div className="orderPager">
          <button type="button" disabled={currentPage <= 1 || loading} onClick={goPrevPage}><ChevronLeft size={15} /><span>上一页</span></button>
          <span className="orderPagerIndex">第 {currentPage} / {totalPages} 页</span>
          <button type="button" disabled={currentPage >= totalPages || loading} onClick={goNextPage}><span>下一页</span><ChevronRight size={15} /></button>
        </div>
      </div>
      {orders.length ? <section className="auctionHistoryList orderReviewList" aria-label="成交处理列表">
        {orders.map((order) => <OrderReviewCard key={order.id} order={order} disabled={detailLoading} onOpen={(nextOrder) => void openDetail(nextOrder)} />)}
      </section> : <StudioEmptyState icon={<Package size={34} />} title="暂无订单" description="当前筛选条件下后端没有返回订单。尝试调整筛选条件或确认本场是否已有落锤拍品。" action={<StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} onClick={() => void syncOrders()}>重新同步</StudioButton>} compact />}
    </section>}
    {detail ? <OrderDetailDrawer detail={detail} loading={detailLoading} onClose={closeDetail} /> : null}
  </section>;
}

function OrderReviewCard({ order, disabled, onOpen }: { order: OrderSummary; disabled: boolean; onOpen: (order: OrderSummary) => void }) {
  const open = () => {
    if (!disabled) onOpen(order);
  };
  const amountLabel = orderAmountLabel(order);
  return <article
    className="historyLotCard orderReviewCard"
    role="button"
    tabIndex={0}
    onClick={open}
    onKeyDown={(event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        open();
      }
    }}
  >
    <div className="historyCardProduct orderReviewProduct">
      <img src={order.lotImageUrl || '/vite.svg'} alt={order.lotTitle || order.lotId || '成交拍品'} loading="lazy" />
      <div>
        <h3>{order.lotTitle || order.lotId}</h3>
        <span>{order.id}</span>
        <small>{order.lotId}</small>
      </div>
    </div>
    <div className="historyCardOutcome orderReviewOutcome">
      <strong><b>订单状态：</b><StudioBadge tone={orderStatusTone(order.status)}>{orderStatusLabel(order.status)}</StudioBadge></strong>
      <div><StudioButton type="button" variant="ghost" size="sm" disabled={disabled} onClick={(event) => { event.stopPropagation(); open(); }}>成交详情</StudioButton></div>
    </div>
    <div className="historyCardMetrics orderReviewMetrics">
      <span><b>{amountLabel}：</b><strong className="orderAmountCell">{formatAmountText(order.amount, order.currency)}</strong></span>
      <span><b>买家：</b>{order.buyerNickname || order.buyerUserId}</span>
      <span><b>创建时间：</b>{formatDateTimeText(order.createdAtUnixMs)}</span>
    </div>
  </article>;
}

function orderAmountLabel(order: OrderSummary) {
  return isOrderPaidStatus(order.status, order.paymentStatus) ? '成交价' : '落锤价';
}

function buyerDisplayName(order?: OrderSummary, fallback = '') {
  return order?.buyerNickname || order?.shippingAddressSnapshot?.receiverName || order?.shippingAddressSnapshot?.receiver || order?.buyerUserId || fallback || '买家未同步';
}

function receiverName(address?: DeliveryAddressSnapshot | null) {
  return address?.receiverName || address?.receiver || '';
}

function formatShippingAddress(address?: DeliveryAddressSnapshot | null, fallback = '') {
  if (!address) return fallback;
  if (address.fullAddress) return address.fullAddress;
  return [address.province, address.city, address.district, address.street, address.detail].filter(Boolean).join('');
}

function OrderDetailDrawer({ detail, loading, onClose }: { detail: LotResultReply; loading: boolean; onClose: () => void }) {
  const order = detail.order;
  const subtitle = order?.id || detail.lot?.id || 'lot result';
  const shippingAddress = order?.shippingAddressSnapshot;
  const addressText = formatShippingAddress(shippingAddress, order?.addressSnapshot || '');
  const buyerName = buyerDisplayName(order, detail.lot?.winnerNickname);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return <div className="orderDetailOverlay" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <aside className="orderDetailDrawer" role="dialog" aria-modal="true" aria-labelledby="order-detail-title" onMouseDown={(event) => event.stopPropagation()}>
      <header className="orderDetailHeader">
        <div><p>Order detail</p><h2 id="order-detail-title">成交详情</h2><span>{subtitle}</span></div>
        <StudioButton type="button" size="sm" variant="ghost" icon={<X size={15} />} onClick={onClose}>关闭</StudioButton>
      </header>
      {loading ? <div className="orderDetailLoading"><StudioTableSkeleton rows={2} columns={2} /></div> : <div className="orderDetailBody">
        <div className="drawerOrderHero"><img src={order?.lotImageUrl || detail.lot?.imageUrl || '/vite.svg'} alt={order?.lotTitle || detail.lot?.title || '成交拍品'} /><div><h3>{order?.lotTitle || detail.lot?.title || '成交拍品'}</h3><strong>{order ? formatAmountText(order.amount, order.currency) : '订单不可见'}</strong><span>{buyerName} · {formatDateTimeText(order?.createdAtUnixMs || detail.lot?.settledAtUnixMs)}</span></div></div>
        <div className="drawerInfoGrid">
          <span>买家姓名：<b>{buyerName}</b></span>
          <span>买家 ID：<b>{order?.buyerUserId || detail.lot?.winnerUserId || '未同步'}</b></span>
          <span>收货人：<b>{receiverName(shippingAddress) || '未填写'}</b></span>
          <span>联系电话：<b>{shippingAddress?.phone || '未填写'}</b></span>
          <span className="isWide">收货地址：<b>{addressText || '未同步收货地址'}</b></span>
          <span>订单状态：<b>{orderStatusLabel(order?.status)}</b></span>
          <span>支付状态：<b>{paymentStatusLabel(order?.paymentStatus)}</b></span>
          <span>竞拍状态：<b>{detail.auctionState || detail.lot?.status || '未同步'}</b></span>
          <span>关联竞拍：<b>{order?.lotId || detail.lot?.id || '未同步'}</b></span>
          <span>支付单号：<b>{order?.paymentId || '未生成 / 不可见'}</b></span>
          <span>支付截止：<b>{formatDateTimeText(order?.expiresAtUnixMs)}</b></span>
        </div>
        <div className="orderDetailFooter">
          {order ? <StudioEmptyState compact tone="success" icon={<ReceiptText size={22} />} title="订单详情来自后端权限接口" description="后台不使用公开 WebSocket reason 中的订单或支付标识。" /> : <StudioErrorState compact icon={<AlertTriangle size={22} />} title="后端未返回订单详情" description="当前账号可能无权查看，或该成交尚未生成订单。" />}
        </div>
      </div>}
    </aside>
  </div>;
}
