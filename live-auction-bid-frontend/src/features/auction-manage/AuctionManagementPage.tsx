import { useMemo, useState } from 'react';
import { AlertTriangle, CheckCircle2, ChevronLeft, ChevronRight, Clock3, Gavel, Package, Radio, RefreshCw, Search, ShieldAlert, ShieldCheck, Trophy, Wifi, X } from 'lucide-react';
import { cancelLot, settleLot, startLot, type AdminLotsQuery } from '../auction/api/auctionApi';
import { CURRENT_LOT_STATUS_FILTERS, canSettleLot, isLiveLot, isPreStartCancellableLot, isQueueReadyLot, isSettlementLot, lotStatusLabel, lotStatusTone, settlementOutcomeDisplay, uiStatusOfLot } from '../../entities/auction/model/auctionStatus';
import type { Lot, RoomSnapshot } from '../../shared/api/types';
import { resultMessage } from '../../shared/api/result';
import { formatDateTimeText, formatDurationText, formatMoneyText } from '../../shared/lib/format';
import { getLotLeftMs, formatAuctionLeftMs } from '../../shared/lib/time';
import { AppLink } from '../../shared/router/AppLink';
import { roomSocketStatusLabel, useRoomSocket } from '../../shared/realtime/useRoomSocket';
import { StudioBadge, StudioButton, StudioCard, StudioEmptyState, StudioErrorState, StudioField, StudioMetricCard, StudioPageHeader, StudioTableSkeleton, StudioToastViewport } from '../../pages/host-console/components/studio-ui';
import { useStudioToast } from '../../pages/host-console/components/studio-toast';
import { useAuctionManagementPage } from './model/useAuctionManagementPage';

const DEFAULT_PAGE_SIZE = 5;

type Props = {
  roomId: string;
  roomName?: string;
};

export function AuctionManagementPage({ roomId, roomName = roomId }: Props) {
  const [realtimeSnapshot, setRealtimeSnapshot] = useState<RoomSnapshot | null>(null);
  const [actionError, setError] = useState('');
  const [selectedLot, setSelectedLot] = useState<Lot | null>(null);
  const [cancelTarget, setCancelTarget] = useState<Lot | null>(null);
  const { toasts, showToast } = useStudioToast();
  const { query, lots, total, snapshot: httpSnapshot, loading, error: queryError, totalPages, currentPage, syncLots: syncLotsQuery, updateQuery, goPrevPage, goNextPage, recoverSnapshot } = useAuctionManagementPage(roomId, DEFAULT_PAGE_SIZE);
  const snapshot = realtimeSnapshot ?? httpSnapshot;
  const error = actionError || queryError;
  const syncLots = async (nextQuery = query) => {
    setError('');
    setRealtimeSnapshot(null);
    try {
      await syncLotsQuery(nextQuery);
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ id: 'admin-lots-sync-failed', tone: 'danger', title: '本场队列同步失败', description: message });
    }
  };

  const startAuction = async (lot: Lot) => {
    setError('');
    try {
      const updated = await startLot(lot.id);
      showToast({ tone: 'success', title: '竞拍已开始', description: updated.title });
      await syncLots();
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '开始竞拍失败', description: message });
    }
  };

  const settleAuction = async (lot: Lot) => {
    setError('');
    const liveLot = currentLot?.id === lot.id ? currentLot : lot;
    if (!canSettleLot(liveLot)) {
      const message = '暂无有效出价，不能落锤成交。请等待买家出价，或使用异常取消处理本件拍品。';
      setError(message);
      showToast({ tone: 'warning', title: '暂不能落锤成交', description: message });
      return;
    }
    try {
      const updated = await settleLot(lot.id);
      showToast({ tone: 'success', title: '已请求落锤成交', description: updated.title });
      await syncLots();
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '落锤成交失败', description: message });
    }
  };

  const confirmCancel = async (lot: Lot, reason: string) => {
    setError('');
    try {
      const updated = await cancelLot(lot.id, reason);
      setCancelTarget(null);
      showToast({ tone: 'success', title: '拍品已取消', description: updated.title });
      await syncLots();
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '取消拍品失败', description: message });
    }
  };

  const socket = useRoomSocket({
    roomId,
    recoverSnapshot: async () => {
      const next = await recoverSnapshot();
      setRealtimeSnapshot(next);
      return next;
    },
    onSnapshot: (nextSnapshot) => setRealtimeSnapshot(nextSnapshot),
    onStatusChange: (status) => {
      if (status === 'connected') {
        setError((current) => current.includes('实时连接') ? '' : current);
      }
    },
    onError: (e, phase) => {
      if (phase === 'socket') return;
      setError(resultMessage(e));
    },
  });

  const currentLot = snapshot?.currentLot || lots.find(isLiveLot) || null;
  const nextLot = lots.find(isQueueReadyLot) || null;
  const wsState = roomSocketStatusLabel(socket.status);
  const pageStart = total ? ((currentPage - 1) * DEFAULT_PAGE_SIZE) + 1 : 0;
  const pageEnd = total ? Math.min(total, currentPage * DEFAULT_PAGE_SIZE) : 0;
  const metrics = useMemo(() => ({
    waiting: lots.filter(isQueueReadyLot).length,
    live: lots.filter(isLiveLot).length,
    settled: lots.filter(isSettlementLot).length,
    abnormal: lots.filter((lot) => lot.status === 'LOT_STATUS_FAILED').length,
  }), [lots]);

  return <section className="auctionMgmtPage">
    <StudioToastViewport toasts={toasts} />
    <StudioCard padding="lg" className="auctionMgmtHeader">
      <StudioPageHeader
        eyebrow="Admin lots"
        title="本场拍品队列"
        actions={<><AppLink className="studioButton studioButton-primary studioButton-md" to="/admin/auctions/create">添加拍品</AppLink><AppLink className="studioButton studioButton-secondary studioButton-md" to="/admin/auctions/history">历史记录</AppLink><StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} loading={loading} onClick={() => void syncLots()}>{loading ? '同步中' : '同步队列'}</StudioButton></>}
      />
    </StudioCard>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    <section className="realtimeSyncCapsule">
      <div><Wifi size={18} /><span>实时同步状态</span><StudioBadge tone={socket.status === 'connected' ? 'success' : socket.status === 'reconnecting' ? 'warning' : 'danger'}>{wsState}</StudioBadge></div>
      <div className="syncCapsuleMetrics"><span>当前直播间：<b>{roomId}</b></span><span>最近心跳：<b>{new Date().toLocaleTimeString('zh-CN', { hour12: false })}</b></span><span>重连次数：<b>{socket.reconnectCount}</b></span><span>当前竞拍：<b>{currentLot?.id || '无 LIVE'}</b></span></div>
      <div><button type="button" onClick={() => void syncLots()}>重新同步</button><AppLink to="/admin/realtime">实时诊断</AppLink></div>
    </section>
    <section className="queueTopCards">
      <QueueFocusCard lot={currentLot} snapshot={snapshot} onCancel={setCancelTarget} />
      <NextLotCard lot={nextLot} disabled={Boolean(currentLot)} onStart={startAuction} />
      <article className="queueTopCard health"><header><span><ShieldCheck size={18} />队列健康</span><StudioBadge tone={socket.status === 'connected' ? 'success' : 'warning'}>{wsState}</StudioBadge></header><div className="queueHealthGrid"><span>返回总数：<b>{total}</b></span><span>本页待拍：<b>{metrics.waiting}</b></span><span>本页落锤：<b>{metrics.settled}</b></span><span>本页异常：<b>{metrics.abnormal}</b></span></div><p>当前直播间 {roomName}</p></article>
    </section>
    <section className="auctionMgmtStats">
      <StudioMetricCard icon={<Clock3 />} label="待开拍" value={metrics.waiting} trend="READY / QUEUED" tone="info" />
      <StudioMetricCard icon={<Radio />} label="进行中" value={metrics.live} trend="LIVE / EXTENDED" tone="success" />
      <StudioMetricCard icon={<Trophy />} label="已落锤" value={metrics.settled} trend="SETTLED" tone="warning" />
      <StudioMetricCard icon={<ShieldAlert />} label="异常" value={metrics.abnormal} trend="FAILED，取消进历史" tone="danger" />
    </section>
    <StudioCard padding="md" className="queueToolbarCard">
      <div className="queueToolbarHeader">
        <div><strong>队列检索</strong><span>每页 5 条，优先保证主播开拍前的扫视和操作准确性。</span></div>
        <div className="queuePageMeta"><span>共 <b>{total}</b> 条</span><span>显示 <b>{pageStart}-{pageEnd}</b></span><span>第 <b>{currentPage}</b> / {totalPages} 页</span></div>
      </div>
      <div className="auctionFilterBar queueFilters" aria-label="拍品队列筛选">
        <label><Search size={15} /><input value={query.keyword || ''} onChange={(e) => updateQuery({ keyword: e.target.value })} onKeyDown={(e) => { if (e.key === 'Enter') void syncLots({ ...query, page: 1 }); }} placeholder="搜索拍品名 / 竞拍 ID" /></label>
        <StudioField label="状态"><select value={query.status || ''} onChange={(e) => updateQuery({ status: e.target.value as AdminLotsQuery['status'] })}>{CURRENT_LOT_STATUS_FILTERS.map((item) => <option key={item.label} value={item.value}>{item.label}</option>)}</select></StudioField>
        <StudioButton type="button" variant="primary" icon={<Search size={15} />} onClick={() => void syncLots({ ...query, page: 1 })}>查询</StudioButton>
        <div className="queuePager" aria-label="拍品队列分页">
          <button type="button" disabled={currentPage <= 1 || loading} onClick={goPrevPage}><ChevronLeft size={15} /><span>上一页</span></button>
          <button type="button" disabled={currentPage >= totalPages || loading} onClick={goNextPage}><span>下一页</span><ChevronRight size={15} /></button>
        </div>
      </div>
    </StudioCard>
    {loading ? <StudioTableSkeleton className="auctionMgmtSkeleton" rows={DEFAULT_PAGE_SIZE} columns={7} /> : error && !lots.length ? <StudioErrorState className="auctionMgmtEmpty" icon={<AlertTriangle size={40} />} title="本场拍品队列加载失败" description={error} action={<StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} onClick={() => void syncLots()}>重试加载</StudioButton>} /> : lots.length ? <section className="auctionQueueList" aria-label="本场拍品队列列表">{lots.map((lot, index) => <AuctionQueueRow key={lot.id} lot={lot} position={((currentPage - 1) * DEFAULT_PAGE_SIZE) + index + 1} currentLot={currentLot} snapshot={snapshot} onDetail={setSelectedLot} onCancel={setCancelTarget} onStart={startAuction} onSettle={settleAuction} />)}</section> : <StudioEmptyState icon={<Package size={34} />} title="暂无拍品" description="当前筛选条件下没有拍品，可以添加新拍品或清空筛选。" action={<AppLink className="studioButton studioButton-primary studioButton-md" to="/admin/auctions/create">添加拍品</AppLink>} />}
    {selectedLot ? <AuctionDetailDrawer lot={selectedLot} snapshot={snapshot} onClose={() => setSelectedLot(null)} /> : null}
    {cancelTarget ? <CancelAuctionDialog lot={cancelTarget} onClose={() => setCancelTarget(null)} onConfirm={confirmCancel} /> : null}
  </section>;
}

function QueueFocusCard({ lot, snapshot, onCancel }: { lot: Lot | null; snapshot: RoomSnapshot | null; onCancel: (lot: Lot) => void }) {
  if (!lot) return <article className="queueTopCard current isEmpty"><header><span><Radio size={18} />当前竞拍</span><StudioBadge tone="neutral">空闲</StudioBadge></header><h3>当前没有正在拍</h3><p>可以从下一件拍品开始，或继续完善今日队列。</p><AppLink className="studioButton studioButton-primary studioButton-sm" to="/admin/auctions/create">添加拍品</AppLink></article>;
  return <article className="queueTopCard current isLive"><header><span><Radio size={18} />当前竞拍</span><StudioBadge tone={lotStatusTone(lot.status)}>{lotStatusLabel(lot.status)}</StudioBadge></header><h3>{lot.title}</h3><div className="queuePriceLine"><span>当前价</span><b>{formatMoneyText(lot.currentPrice)}</b><small>{formatAuctionLeftMs(getLotLeftMs(lot, snapshot?.serverTimeUnixMs), 'queue')}</small></div><p>领先用户：{lot.leadingNickname || '暂无'} · 出价 {snapshot?.recentBids?.length || 0} 次</p><div className="queueTopActions"><AppLink className="studioButton studioButton-primary studioButton-sm" to={`/admin/auctions/${lot.id}/control`}>进入中控台</AppLink><button type="button" className="studioButton studioButton-danger studioButton-sm" onClick={() => onCancel(lot)}>异常取消</button></div></article>;
}

function NextLotCard({ lot, disabled, onStart }: { lot: Lot | null; disabled: boolean; onStart: (lot: Lot) => void }) {
  if (!lot) return <article className="queueTopCard next isEmpty"><header><span><Gavel size={18} />下一件拍品</span><StudioBadge tone="neutral">暂无</StudioBadge></header><h3>待开拍为空</h3><p>添加拍品后会进入本场队列。</p></article>;
  return <article className="queueTopCard next hasNext"><header><span><Gavel size={18} />下一件拍品</span><StudioBadge tone={lotStatusTone(lot.status)}>{lotStatusLabel(lot.status)}</StudioBadge></header><h3>{lot.title}</h3><div className="queueRulePills"><span>起拍：<b>{formatMoneyText(lot.rule.startPrice)}</b></span><span>加价：<b>{formatMoneyText(lot.rule.minIncrement)}</b></span><span>封顶：<b>{formatMoneyText(lot.rule.capPrice)}</b></span></div><p>预计 {formatDurationText(lot.rule.durationSeconds)} · 等待运营确认开拍节奏</p><div className="queueTopActions"><button type="button" className="studioButton studioButton-secondary studioButton-sm" disabled={disabled} onClick={() => void onStart(lot)}>{disabled ? '等待当前结束' : '开始竞拍'}</button></div></article>;
}

function AuctionQueueRow({ lot, position, currentLot, snapshot, onDetail, onCancel, onStart, onSettle }: { lot: Lot; position: number; currentLot: Lot | null; snapshot: RoomSnapshot | null; onDetail: (lot: Lot) => void; onCancel: (lot: Lot) => void; onStart: (lot: Lot) => void; onSettle: (lot: Lot) => void }) {
  const status = uiStatusOfLot(lot);
  const isCurrent = Boolean(currentLot?.id === lot.id || isLiveLot(lot));
  const isNext = !isCurrent && isQueueReadyLot(lot);
  const liveLot = currentLot?.id === lot.id ? currentLot : lot;
  const settleReady = canSettleLot(liveLot);
  return <article className={`queueRowCard ${isCurrent ? 'isCurrent' : ''} ${isNext ? 'isNext' : ''} ${status === '已取消' ? 'isCancelled' : ''}`} onClick={() => onDetail(lot)}>
    <div className="queueRowLeft"><span className="queueNo">#{String(position).padStart(2, '0')}</span><img src={lot.imageUrl || '/vite.svg'} alt={lot.title} /><div><h3>{lot.title}</h3><div className="queueTags"><StudioBadge tone={lotStatusTone(lot.status)}>{lotStatusLabel(lot.status)}</StudioBadge><span>竞拍 ID {lot.id}</span><span>规则 v{lot.version || 1}</span></div></div></div>
    <div className="queueRowMiddle"><span><b>状态进度：</b>{statusProgressText(lot, snapshot)}</span><span><b>开拍时间：</b>{formatDateTimeText(lot.startedAtUnixMs, '未开拍')}</span><span><b>起拍 / 加价：</b>{formatMoneyText(lot.rule.startPrice)} / {formatMoneyText(lot.rule.minIncrement)}</span><span><b>封顶 / 时长：</b>{formatMoneyText(lot.rule.capPrice)} / {formatDurationText(lot.rule.durationSeconds)}</span></div>
    <div className="queueRowRight">{orderStateText(lot)}<div className="auctionRowActions" onClick={(e) => e.stopPropagation()}><button type="button" className="queueActionPlain" onClick={() => onDetail(lot)}>详情</button>{isQueueReadyLot(lot) ? <button type="button" className="queueActionPrimary" disabled={Boolean(currentLot)} onClick={() => void onStart(lot)}>开始竞拍</button> : null}{isLiveLot(lot) ? <><AppLink className="queueActionPrimary" to={`/admin/auctions/${lot.id}/control`}>进入中控</AppLink><button type="button" className="queueActionPrimary" disabled={!settleReady} title={settleReady ? '落锤成交' : '暂无有效出价，不能落锤成交'} onClick={() => void onSettle(lot)}>{settleReady ? '落锤成交' : '等待出价'}</button><button type="button" className="queueActionDanger danger" onClick={() => onCancel(lot)}>异常取消</button></> : null}{isPreStartCancellableLot(lot) ? <button type="button" className="queueActionDanger danger" onClick={() => onCancel(lot)}>取消拍品</button> : null}{isSettlementLot(lot) ? <AppLink className="queueActionPrimary" to="/admin/orders">成交处理</AppLink> : null}</div></div>
  </article>;
}

function statusProgressText(lot: Lot, snapshot: RoomSnapshot | null) {
  if (isLiveLot(lot)) return `倒计时 ${formatAuctionLeftMs(getLotLeftMs(lot, snapshot?.serverTimeUnixMs), 'queue')}`;
  if (isSettlementLot(lot)) return `落锤时间 ${formatDateTimeText(lot.settledAtUnixMs)}`;
  if (lot.status === 'LOT_STATUS_CANCELLED') return lot.cancelReason || '已取消';
  return `状态 ${lotStatusLabel(lot.status)}`;
}

function orderStateText(lot: Lot) {
  if (isSettlementLot(lot)) {
    const outcome = settlementOutcomeDisplay(lot);
    return <div className={`orderState ${outcome.state === 'failed' ? 'danger' : ''}`}><b>{outcome.label}</b><span>{outcome.priceLabel} {formatMoneyText(lotResultMoney(lot))}</span></div>;
  }
  if (isLiveLot(lot)) return <div className="orderState"><b>等待成交</b><span>{lot.leadingNickname || '暂无领先用户'}</span></div>;
  if (lot.status === 'LOT_STATUS_CANCELLED') return <div className="orderState danger"><b>取消原因</b><span>{lot.cancelReason || '已取消'}</span></div>;
  return <span className="mutedText">未成交</span>;
}

function drawerPersonText(lot: Lot) {
  if (isLiveLot(lot)) return `领先用户：${lot.leadingNickname || '暂无'}`;
  if (isSettlementLot(lot)) {
    const outcome = settlementOutcomeDisplay(lot);
    return `${outcome.personLabel}：${lot.winnerNickname || lot.winnerUserId || '买家未同步'}`;
  }
  if (lot.status === 'LOT_STATUS_CANCELLED') return `取消原因：${lot.cancelReason || '已取消'}`;
  return '状态同步中';
}

function drawerPrimaryPrice(lot: Lot) {
  return lotResultMoney(lot);
}

function lotResultMoney(lot: Lot) {
  if (isSettlementLot(lot) && Number(lot.finalPrice?.amount || 0) > 0) return lot.finalPrice;
  if (Number(lot.currentPrice?.amount || 0) > 0) return lot.currentPrice;
  return lot.rule.startPrice;
}

export function AuctionDetailDrawer({ lot, snapshot, onClose }: { lot: Lot; snapshot: RoomSnapshot | null; onClose: () => void }) {
  const bids = snapshot?.currentLot?.id === lot.id ? snapshot.recentBids : [];
  const outcome = isSettlementLot(lot) ? settlementOutcomeDisplay(lot) : null;
  const media = [lot.imageUrl, ...(lot.galleryImageUrls ?? [])].filter(Boolean);
  const isCurrentLot = snapshot?.currentLot?.id === lot.id;
  const bidEmptyDescription = isCurrentLot ? '当前房间快照暂未同步 recentBids。' : '该拍品不是当前开拍项，实时出价只在直播中拍品展示。';
  const rules: [string, string][] = [
    ['起拍价', formatMoneyText(lot.rule.startPrice)],
    ['加价幅度', formatMoneyText(lot.rule.minIncrement)],
    ['竞拍时长', formatDurationText(lot.rule.durationSeconds)],
    ['封顶价', formatMoneyText(lot.rule.capPrice)],
    ['延时窗口', `${lot.rule.antiSnipeWindowSeconds}s`],
    ['最大延时', `${lot.rule.maxExtendCount}`],
  ];

  return (
    <aside className="auctionDrawer auctionDetailDrawer" role="dialog" aria-modal="true" aria-label="拍品详情">
      <div className="drawerMask" onClick={onClose} />
      <section className="auctionDetailPanel">
        <header className="auctionDetailHeader">
          <div>
            <p>竞拍详情</p>
            <h3>{lot.title}</h3>
            <span>{lot.id}</span>
          </div>
          <button type="button" className="auctionDetailClose" onClick={onClose} aria-label="关闭详情">
            <X size={17} />
            <span>关闭</span>
          </button>
        </header>

        <div className="auctionDetailHero">
          <div className="auctionDetailMedia">
            {media[0] ? <img src={media[0]} alt={lot.title} /> : <Package size={34} />}
          </div>
          <div className="auctionDetailSummary">
            <StudioBadge tone={outcome?.tone ?? lotStatusTone(lot.status)}>{outcome?.label ?? lotStatusLabel(lot.status)}</StudioBadge>
            <strong>{formatMoneyText(drawerPrimaryPrice(lot))}</strong>
            <p>{lot.description || '暂无描述'}</p>
            <div>
              <span>{drawerPersonText(lot)}</span>
              <span>规则版本：v{lot.version || 1}</span>
            </div>
          </div>
        </div>

        <div className="auctionDetailStats" aria-label="拍品数据">
          <div><span>分类</span><b>{lot.category || '未分类'}</b></div>
          <div><span>库存</span><b>{lot.stock || 1} 件</b></div>
          <div><span>出价</span><b>{lot.stats?.bidCount ?? 0} 次</b></div>
          <div><span>参与</span><b>{lot.stats?.participantCount ?? 0} 人</b></div>
        </div>

        <section className="auctionDetailSection">
          <h4>竞拍规则</h4>
          <div className="ruleSnapshotGrid">
            {rules.map(([label, value]) => <div key={label}><span>{label}</span><b>{value}</b></div>)}
          </div>
        </section>

        <section className="auctionDetailSection">
          <h4>实时出价</h4>
          <div className="drawerBidList">
            {bids.length ? bids.map((bid) => (
              <div key={bid.id}>
                <span>{bid.nickname || bid.userId}</span>
                <b>{formatMoneyText(bid.amount)}</b>
                <small>{formatDateTimeText(bid.createdAtUnixMs)} · {bid.userId === lot.leadingUserId ? '领先' : '非领先'}</small>
              </div>
            )) : <StudioEmptyState compact icon={<CheckCircle2 size={22} />} title="暂无实时出价" description={bidEmptyDescription} />}
          </div>
        </section>
      </section>
    </aside>
  );
}

function CancelAuctionDialog({ lot, onClose, onConfirm }: { lot: Lot; onClose: () => void; onConfirm: (lot: Lot, reason: string) => Promise<void> }) {
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const liveCancel = isLiveLot(lot);
  const submit = async () => {
    if (!reason.trim()) return;
    setSubmitting(true);
    try { await onConfirm(lot, reason.trim()); } finally { setSubmitting(false); }
  };
  return <div className="cancelDialog"><div onClick={onClose} /><section><header><AlertTriangle size={22} /><div><h3>{liveCancel ? '异常取消直播中拍品' : '取消拍品'}</h3><p>{liveCancel ? '取消后会立即关闭本件竞拍、禁用观众出价并广播直播间。' : '取消后会写入后端并广播当前直播间。'}</p></div></header><b>{lot.title}</b><textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={4} placeholder={liveCancel ? '请输入异常取消原因，例如主播网络异常、商品信息有误' : '请输入取消原因，例如误加入队列、资料需要重填'} /><footer><button type="button" onClick={onClose}>返回</button><button type="button" className="danger" disabled={!reason.trim() || submitting} onClick={() => void submit()}>{submitting ? '提交中...' : liveCancel ? '确认异常取消' : '确认取消拍品'}</button></footer></section></div>;
}
