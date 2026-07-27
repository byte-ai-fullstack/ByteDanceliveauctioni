import { useCallback, useMemo, useState } from 'react';
import { AlertTriangle, RefreshCw } from 'lucide-react';
import type { Lot, RoomSnapshot } from '../../shared/api/types';
import { resultMessage } from '../../shared/api/result';
import { formatDateTimeText } from '../../shared/lib/format';
import { AppLink } from '../../shared/router/AppLink';
import { useRoomSocket } from '../../shared/realtime/useRoomSocket';
import { StudioButton, StudioCard, StudioPageHeader, StudioTableSkeleton } from '../../pages/host-console/components/studio-ui';
import { AuctionDetailDrawer } from './AuctionManagementPage';
import { buildDashboardAnalytics, type DashboardRange } from './model/dashboardAnalytics';
import { useAdminDashboardPage } from './model/useAdminDashboardPage';
import { CategoryPerformancePanel, FunnelChart, LotRanking, TrendChart } from './ui/DashboardCharts';
import { MetricGrid } from './ui/DashboardMetrics';
import { ActionChecklist, LiveBusinessPanel, LotPerformanceTable, OrderRiskPanel } from './ui/DashboardPanels';
import { useScrollReveal } from './ui/useViewAnimations';
import './admin-dashboard.css';

const VIEW_ANIMATION_TRIGGER_RATIO = 0.72;

const rangeOptions: Array<{ value: DashboardRange; label: string; detail: string }> = [
  { value: 'today', label: '今日', detail: '当天成交' },
  { value: 'live', label: '本场', detail: '当前直播间' },
  { value: '7d', label: '近7天', detail: '滚动一周' },
  { value: '30d', label: '近30天', detail: '默认视图' },
];

export function AdminDashboardPage({ roomId, roomName = roomId }: { roomId: string; roomName?: string }) {
  const [range, setRange] = useState<DashboardRange>('30d');
  const [realtimeSnapshot, setRealtimeSnapshot] = useState<RoomSnapshot | null>(null);
  const [snapshotReceivedAt, setSnapshotReceivedAt] = useState(0);
  const [realtimeError, setError] = useState('');
  const [selectedLot, setSelectedLot] = useState<Lot | null>(null);
  const { snapshot: httpSnapshot, lots, orders, loading, error: queryError, lastUpdatedAt, refreshSeq, sync: syncQuery, recoverSnapshot } = useAdminDashboardPage(roomId);
  const snapshot = realtimeSnapshot ?? httpSnapshot;
  const error = realtimeError || queryError;

  const commitSnapshot = useCallback((next: RoomSnapshot) => {
    setRealtimeSnapshot(next);
    setSnapshotReceivedAt(Date.now());
  }, []);

  const sync = useCallback(async (silent = false) => {
    setError('');
    if (!silent) setRealtimeSnapshot(null);
    try {
      await syncQuery();
    } catch (e) {
      setError(resultMessage(e));
    }
  }, [syncQuery]);

  useRoomSocket({
    roomId,
    recoverSnapshot: async () => {
      const next = await recoverSnapshot();
      commitSnapshot(next);
      return next;
    },
    onSnapshot: commitSnapshot,
    onError: (e) => setError(resultMessage(e)),
  });

  const analyticsNowMs = Number(snapshot?.serverTimeUnixMs || lastUpdatedAt || 0);
  const analytics = useMemo(() => buildDashboardAnalytics({ lots, orders, snapshot, range, nowMs: analyticsNowMs }), [analyticsNowMs, lots, orders, snapshot, range]);
  const currentLot = snapshot?.currentLot ?? null;
  const receivedAt = realtimeSnapshot ? snapshotReceivedAt : lastUpdatedAt;
  const recentBids = useMemo(() => [...(snapshot?.recentBids ?? [])].sort((a, b) => Number(b.createdAtUnixMs || 0) - Number(a.createdAtUnixMs || 0)).slice(0, 5), [snapshot]);
  const initialLoading = loading && !lots.length && !orders.length;
  const [chartGridRef, chartGridVisible] = useScrollReveal<HTMLElement>(VIEW_ANIMATION_TRIGGER_RATIO);
  const [performanceGridRef, performanceGridVisible] = useScrollReveal<HTMLElement>(VIEW_ANIMATION_TRIGGER_RATIO);
  const [riskGridRef, riskGridVisible] = useScrollReveal<HTMLElement>(VIEW_ANIMATION_TRIGGER_RATIO);

  return <section className="merchantDashboardPage">
    <StudioCard padding="lg" className="merchantDashboardHero">
      <StudioPageHeader
        eyebrow="Business cockpit"
        title="主播/商家经营数据看板"
        description="围绕成交、支付、拍品表现和待处理风险查看当前商家经营情况。"
        actions={<div className="merchantHeroActions">
          <AppLink className="studioButton studioButton-primary studioButton-md" to="/admin/auctions/create">添加拍品</AppLink>
          <AppLink className="studioButton studioButton-secondary studioButton-md" to="/admin/orders">处理订单</AppLink>
          <StudioButton type="button" variant="soft" icon={<RefreshCw size={15} />} loading={loading} onClick={() => void sync()}>刷新</StudioButton>
        </div>}
      />
      <div className="merchantRangeBar" aria-label="经营数据时间范围">
        {rangeOptions.map((option) => <button key={option.value} type="button" className={range === option.value ? 'active' : ''} aria-pressed={range === option.value} onClick={() => setRange(option.value)}>
          <b>{option.label}</b>
          <span>{option.detail}</span>
        </button>)}
      </div>
      <div className="merchantHeroMeta">
        <span>当前直播间：<b>{roomName}</b></span>
        <span>数据范围：<b>{rangeOptions.find((item) => item.value === range)?.label}</b></span>
        <span>更新时间：<b>{lastUpdatedAt ? formatDateTimeText(lastUpdatedAt, '刚刚') : '加载中'}</b></span>
      </div>
    </StudioCard>

    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}

    {initialLoading ? <StudioTableSkeleton rows={5} columns={4} /> : <>
      <MetricGrid analytics={analytics} />

      <section className="merchantTopGrid">
        <LiveBusinessPanel currentLot={currentLot} recentBids={recentBids} serverTimeUnixMs={Number(snapshot?.serverTimeUnixMs || 0)} snapshotReceivedAt={receivedAt} participantCount={currentLot?.stats.participantCount ?? 0} />
        <ActionChecklist analytics={analytics} />
      </section>

      <section ref={chartGridRef} className={`merchantChartGrid merchantViewAnimate${chartGridVisible ? ' isVisible' : ''}`}>
        <StudioCard title="成交漏斗" subtitle="Auction funnel" className="merchantPanel merchantFunnelPanel">
          <FunnelChart steps={analytics.funnel} refreshSeq={refreshSeq} />
        </StudioCard>
        <StudioCard title="GMV / 支付趋势" subtitle="Revenue trend" className="merchantPanel merchantTrendPanel">
          <TrendChart key={`${range}-${refreshSeq}`} buckets={analytics.timeSeries} refreshSeq={`${range}-${refreshSeq}`} />
        </StudioCard>
      </section>

      <section ref={performanceGridRef} className={`merchantPerformanceGrid merchantViewAnimate${performanceGridVisible ? ' isVisible' : ''}`}>
        <StudioCard title="成交额 Top 10" subtitle="Lot ranking" className="merchantPanel">
          <LotRanking lots={analytics.topLots} />
        </StudioCard>
        <StudioCard title="品类表现" subtitle="Category performance" className="merchantPanel">
          <CategoryPerformancePanel lots={analytics.lotPerformance} />
        </StudioCard>
      </section>

      <section ref={riskGridRef} className={`merchantRiskGrid merchantViewAnimate${riskGridVisible ? ' isVisible' : ''}`}>
        <StudioCard title="订单风险" subtitle="Order risk" className="merchantPanel">
          <OrderRiskPanel analytics={analytics} />
        </StudioCard>
        <StudioCard title="拍品表现明细" subtitle="Lot table" className="merchantPanel merchantLotTablePanel">
          <LotPerformanceTable lots={analytics.lotPerformance} refreshSeq={refreshSeq} onOpenLot={setSelectedLot} />
        </StudioCard>
      </section>
    </>}
    {selectedLot ? <AuctionDetailDrawer lot={selectedLot} snapshot={snapshot} onClose={() => setSelectedLot(null)} /> : null}
  </section>;
}
