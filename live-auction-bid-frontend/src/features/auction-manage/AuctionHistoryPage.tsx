import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { AlertTriangle, BadgeCheck, ChevronLeft, ChevronRight, Clock3, FileClock, Image as ImageIcon, Package, RefreshCw, Search, ShieldAlert, Trophy, X, XCircle } from 'lucide-react';
import { AppLink } from '../../shared/router/AppLink';
import type { AdminLotsQuery } from '../auction/api/auctionApi';
import { HISTORY_LOT_STATUS_FILTERS, isSettlementLot, lotStatusLabel, lotStatusTone, settlementOutcomeDisplay } from '../../entities/auction/model/auctionStatus';
import type { OrderSummary } from '../../entities/order/model/orderTypes';
import type { Lot } from '../../shared/api/types';
import { formatDateTimeText, formatDurationText, formatMoneyText } from '../../shared/lib/format';
import { StudioBadge, StudioButton, StudioCard, StudioEmptyState, StudioErrorState, StudioField, StudioMetricCard, StudioPageHeader, StudioTableSkeleton } from '../../pages/host-console/components/studio-ui';
import { useAuctionHistoryPage } from './model/useAuctionHistoryPage';

const DEFAULT_PAGE_SIZE = 10;

export function AuctionHistoryPage({ roomId }: { roomId: string }) {
  const [selectedLot, setSelectedLot] = useState<Lot | null>(null);
  const { query, lots, ordersByLotId, total, loading, error, totalPages, currentPage, goPrevPage, goNextPage, syncLots, updateQuery } = useAuctionHistoryPage(roomId, DEFAULT_PAGE_SIZE);

  const metrics = useMemo(() => ({
    settled: lots.filter(isSettlementLot).length,
    cancelled: lots.filter((lot) => lot.status === 'LOT_STATUS_CANCELLED').length,
    failed: lots.filter((lot) => lot.status === 'LOT_STATUS_FAILED').length,
  }), [lots]);

  return <section className="postLivePage auctionHistoryPage">
    <StudioCard padding="lg" className="postLiveHeader auctionHistoryHero">
      <StudioPageHeader
        eyebrow="Lot history"
        title="拍品历史记录"
        description="已落锤、已取消和异常拍品从本场队列拆出，保留给运营复盘、订单核对和取消原因审计。"
        actions={<><AppLink className="studioButton studioButton-secondary studioButton-md" to="/admin/auctions">返回本场队列</AppLink><StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} loading={loading} onClick={() => void syncLots()}>{loading ? '同步中' : '刷新历史'}</StudioButton></>}
      />
    </StudioCard>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    <section className="postLiveMetricGrid auctionHistoryMetrics">
      <StudioMetricCard icon={<FileClock />} label="历史总数" value={total} trend="当前筛选结果" tone="info" />
      <StudioMetricCard icon={<Trophy />} label="本页落锤" value={metrics.settled} trend="SETTLED" tone="warning" />
      <StudioMetricCard icon={<XCircle />} label="本页取消" value={metrics.cancelled} trend="运营撤回" tone="warning" />
      <StudioMetricCard icon={<ShieldAlert />} label="本页异常" value={metrics.failed} trend="FAILED" tone="danger" />
    </section>
    <StudioCard padding="md" className="postLiveHeader auctionHistoryFiltersCard">
      <div className="auctionFilterBar queueFilters" aria-label="拍品历史筛选">
        <label><Search size={15} /><input value={query.keyword || ''} onChange={(e) => updateQuery({ keyword: e.target.value })} onKeyDown={(e) => { if (e.key === 'Enter') void syncLots({ ...query, page: 1 }); }} placeholder="搜索拍品名 / 竞拍 ID / 取消原因" /></label>
        <StudioField label="历史状态"><select value={query.status || ''} onChange={(e) => updateQuery({ status: e.target.value as AdminLotsQuery['status'] })}>{HISTORY_LOT_STATUS_FILTERS.map((item) => <option key={item.label} value={item.value}>{item.label}</option>)}</select></StudioField>
        <StudioButton type="button" variant="primary" icon={<Search size={15} />} onClick={() => void syncLots({ ...query, page: 1 })}>查询</StudioButton>
      </div>
    </StudioCard>
    {loading ? <StudioTableSkeleton rows={DEFAULT_PAGE_SIZE} columns={7} /> : error && !lots.length ? <StudioErrorState icon={<AlertTriangle size={34} />} title="历史记录加载失败" description={error} action={<StudioButton type="button" variant="secondary" onClick={() => void syncLots()}>重试</StudioButton>} /> : <section className="auctionHistoryListWrap">
      <div className="historyListHeader">
        <strong>共 {total} 条 · 每页 {DEFAULT_PAGE_SIZE} 条</strong>
        <div className="orderPager">
          <button type="button" disabled={currentPage <= 1 || loading} onClick={goPrevPage}><ChevronLeft size={15} /><span>上一页</span></button>
          <span className="orderPagerIndex">第 {currentPage} / {totalPages} 页</span>
          <button type="button" disabled={currentPage >= totalPages || loading} onClick={goNextPage}><span>下一页</span><ChevronRight size={15} /></button>
        </div>
      </div>
      {lots.length ? <section className="auctionHistoryList" aria-label="拍品历史列表">
        {lots.map((lot) => <HistoryLotCard key={lot.id} lot={lot} order={ordersByLotId[lot.id]} onOpen={setSelectedLot} />)}
      </section> : <StudioEmptyState icon={<Package size={34} />} title="暂无历史记录" description="当前筛选条件下没有已落锤、已取消或异常拍品。" action={<AppLink className="studioButton studioButton-secondary studioButton-md" to="/admin/auctions">返回本场队列</AppLink>} compact />}
    </section>}
    {selectedLot ? <HistoryLotDetailDrawer lot={selectedLot} order={ordersByLotId[selectedLot.id]} onClose={() => setSelectedLot(null)} /> : null}
  </section>;
}

function historyTimeText(lot: Lot) {
  if (lot.status === 'LOT_STATUS_CANCELLED') return formatDateTimeText(lot.cancelledAtUnixMs);
  if (isSettlementLot(lot)) return formatDateTimeText(lot.settledAtUnixMs);
  return formatDateTimeText(lot.startedAtUnixMs, '未记录');
}

function historyReasonText(lot: Lot) {
  return <span className="historyReasonText">{historyReasonValue(lot)}</span>;
}

function historyReasonValue(lot: Lot) {
  if (lot.status === 'LOT_STATUS_CANCELLED') return `取消原因：${lot.cancelReason || '未填写取消原因'}`;
  if (isSettlementLot(lot)) return `竞得者：${lot.winnerNickname || lot.winnerUserId || '买家未同步'}`;
  return '异常状态：待运营复核';
}

function historyBuyerIdText(lot: Lot) {
  if (!isSettlementLot(lot)) return '无';
  return lot.winnerUserId || '买家未同步';
}

function historyTimeLabel(lot: Lot) {
  if (lot.status === 'LOT_STATUS_CANCELLED') return '取消时间';
  if (isSettlementLot(lot)) return '落锤时间';
  return '结束时间';
}

function historyStatusLabel(lot: Lot, order?: OrderSummary | null) {
  if (!isSettlementLot(lot)) return lotStatusLabel(lot.status);
  return settlementOutcomeDisplay(lot, order).label;
}

function historyStatusTone(lot: Lot, order?: OrderSummary | null) {
  if (!isSettlementLot(lot)) return lotStatusTone(lot.status);
  return settlementOutcomeDisplay(lot, order).tone;
}

function historyPriceLabel(lot: Lot, order?: OrderSummary | null) {
  if (!isSettlementLot(lot)) return '结果价';
  return settlementOutcomeDisplay(lot, order).priceLabel;
}

function HistoryLotCard({ lot, order, onOpen }: { lot: Lot; order?: OrderSummary | null; onOpen: (lot: Lot) => void }) {
  const price = resultPrice(lot);
  const insights = historyInsightItems(lot).slice(0, 2);
  const open = () => onOpen(lot);
  return <article
    className={`historyLotCard ${lot.status === 'LOT_STATUS_CANCELLED' ? 'isCancelled' : ''} ${lot.status === 'LOT_STATUS_FAILED' ? 'isFailed' : ''}`}
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
    <div className="historyCardProduct">
      <img src={lot.imageUrl || '/vite.svg'} alt={lot.title || '历史拍品'} />
      <div>
        <h3>{lot.title || '未命名拍品'}</h3>
        <div className="historyCardTags">
          <StudioBadge tone={historyStatusTone(lot, order)}>{historyStatusLabel(lot, order)}</StudioBadge>
        </div>
      </div>
    </div>
    <div className="historyCardMetrics">
      <span><b>{historyPriceLabel(lot, order)}：</b>{formatMoneyText(price)}</span>
      <span><b>买家 ID：</b>{historyBuyerIdText(lot)}</span>
      <span><b>{historyTimeLabel(lot)}：</b>{historyTimeText(lot)}</span>
      <span><b>起拍价：</b>{formatMoneyText(lot.rule.startPrice)}</span>
      <span><b>最低加价要求：</b>{formatMoneyText(lot.rule.minIncrement)}</span>
      <span><b>耗时：</b>{actualDurationText(lot)}</span>
    </div>
    <div className="historyCardOutcome">
      <strong>{historyReasonText(lot)}</strong>
      <small>{formatDeltaText(lot)} · {premiumRateText(lot)}</small>
      <div>{insights.map((item) => <em key={item.title} className={`historyInsightPill ${item.tone}`}>{item.title}</em>)}</div>
    </div>
  </article>;
}

function HistoryLotDetailDrawer({ lot, order, onClose }: { lot: Lot; order?: OrderSummary | null; onClose: () => void }) {
  const insights = historyInsightItems(lot);
  const gallery = Array.from(new Set(lot.galleryImageUrls || [])).filter((imageUrl) => imageUrl && imageUrl !== lot.imageUrl);
  const tags = lot.tags || [];

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return <div className="historyDetailOverlay" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <aside className="historyDetailDrawer" role="dialog" aria-modal="true" aria-labelledby="history-detail-title" onMouseDown={(event) => event.stopPropagation()}>
      <header className="historyDetailHeader">
        <div><p>Lot review</p><h2 id="history-detail-title">拍品复盘详情</h2><span>{lot.id}</span></div>
        <StudioButton type="button" size="sm" variant="ghost" icon={<X size={15} />} onClick={onClose}>关闭</StudioButton>
      </header>
      <div className="historyDetailBody">
        <section className="historyDetailHero">
          <img src={lot.imageUrl || '/vite.svg'} alt={lot.title || '历史拍品'} />
          <div>
            <StudioBadge tone={historyStatusTone(lot, order)}>{historyStatusLabel(lot, order)}</StudioBadge>
            <h3>{lot.title || '未命名拍品'}</h3>
            <strong>{formatMoneyText(resultPrice(lot))}</strong>
            <span>{historyReasonValue(lot)} · {historyTimeText(lot)}</span>
          </div>
        </section>
        <section className="historyInsightList" aria-label="复盘提示">
          {insights.map((item) => <article key={item.title} className={item.tone}><b>{item.title}</b><span>{item.detail}</span></article>)}
        </section>
        <div className="historyDetailGrid">
          <HistoryDetailSection icon={<Trophy size={16} />} title="竞拍结果">
            <DetailMetric label={historyPriceLabel(lot, order)} value={formatMoneyText(resultPrice(lot))} />
            <DetailMetric label="起拍价" value={formatMoneyText(lot.rule.startPrice)} />
            <DetailMetric label="最低加价要求" value={formatMoneyText(lot.rule.minIncrement)} />
            <DetailMetric label="封顶价" value={formatMoneyText(lot.rule.capPrice)} />
            <DetailMetric label="价差" value={formatDeltaText(lot)} />
            <DetailMetric label="溢价率" value={premiumRateText(lot)} />
            <DetailMetric label="参考估价" value={formatMoneyText(lot.estimatePrice)} />
            <DetailMetric label="保证金" value={formatMoneyText(lot.depositAmount)} />
            <DetailMetric label="买家 / 原因" value={historyReasonValue(lot)} wide />
          </HistoryDetailSection>
          <HistoryDetailSection icon={<Clock3 size={16} />} title="主播复盘">
            <DetailMetric label="开拍时间" value={formatDateTimeText(lot.startedAtUnixMs, '未开拍')} />
            <DetailMetric label="结论时间" value={historyTimeText(lot)} />
            <DetailMetric label="实际耗时" value={actualDurationText(lot)} />
            <DetailMetric label="配置时长" value={formatDurationText(lot.rule.durationSeconds)} />
            <DetailMetric label="延时窗口" value={`${Number(lot.rule.antiSnipeWindowSeconds || 0)} 秒`} />
            <DetailMetric label="单次延时" value={`${Number(lot.rule.antiSnipeExtendSeconds || 0)} 秒`} />
            <DetailMetric label="延时次数" value={`${Number(lot.duelState?.extendCount || 0)} / ${Number(lot.rule.maxExtendCount || lot.duelState?.maxExtendCount || 0)}`} />
          </HistoryDetailSection>
          <HistoryDetailSection icon={<ImageIcon size={16} />} title="拍品资料">
            <DetailMetric label="分类" value={lot.category || '未分类'} />
            <DetailMetric label="库存" value={lot.stock ? `${lot.stock} 件` : '未设置'} />
            <DetailMetric label="标签" value={tags.length ? tags.join(' / ') : '未设置'} wide />
            <DetailMetric label="拍品描述" value={lot.description || '暂无描述'} wide />
            <DetailMetric label="售后说明" value={lot.afterSaleNotes || '未填写'} wide />
            <div className="historyGallery" aria-label="拍品轮播图">{gallery.length ? gallery.map((imageUrl, index) => <img key={`${imageUrl}-${index}`} src={imageUrl} alt={`${lot.title || '拍品'}轮播图 ${index + 1}`} />) : <span>未上传轮播图</span>}</div>
          </HistoryDetailSection>
          <HistoryDetailSection icon={<BadgeCheck size={16} />} title="讲解资料">
            {(lot.trustCards || []).length ? <div className="historyTrustCardList">
              {lot.trustCards.map((card) => <article key={card.id || `${card.type}-${card.title}`}>
                {card.imageUrl ? <img src={card.imageUrl} alt={card.title || '讲解卡'} /> : null}
                <div><b>{card.title || card.type || '讲解卡'}</b><span>{card.content || '未填写讲解内容'}</span></div>
              </article>)}
            </div> : <StudioEmptyState compact icon={<BadgeCheck size={22} />} title="未配置讲解卡" description="历史详情仍可用于价格、时长和取消原因复盘。" />}
          </HistoryDetailSection>
        </div>
      </div>
    </aside>
  </div>;
}

function HistoryDetailSection({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return <section className="historyDetailSection"><header>{icon}<h3>{title}</h3></header><div>{children}</div></section>;
}

function DetailMetric({ label, value, wide = false }: { label: string; value: ReactNode; wide?: boolean }) {
  return <span className={wide ? 'wide' : ''}>{label}<b>{value}</b></span>;
}

function resultPrice(lot: Lot) {
  if (isSettlementLot(lot) && moneyAmount(lot.finalPrice) > 0) return lot.finalPrice;
  if (moneyAmount(lot.currentPrice) > 0) return lot.currentPrice;
  return lot.rule.startPrice;
}

function moneyAmount(value?: { amount?: number | string | null } | null) {
  const amount = Number(value?.amount || 0);
  return Number.isFinite(amount) ? amount : 0;
}

function moneyCurrency(lot: Lot) {
  return resultPrice(lot).currency || lot.rule.startPrice?.currency || 'CNY';
}

function formatCentAmount(amount: number, currency: string) {
  return formatMoneyText({ amount, currency });
}

function formatDeltaText(lot: Lot) {
  const start = moneyAmount(lot.rule.startPrice);
  const result = moneyAmount(resultPrice(lot));
  if (result <= 0) return '价格未记录';
  if (start <= 0) return `高于起拍 ${formatCentAmount(result, moneyCurrency(lot))}`;
  const diff = result - start;
  if (diff === 0) return '与起拍价持平';
  return `${diff > 0 ? '高于起拍' : '低于起拍'} ${formatCentAmount(Math.abs(diff), moneyCurrency(lot))}`;
}

function premiumRateText(lot: Lot) {
  const start = moneyAmount(lot.rule.startPrice);
  const result = moneyAmount(resultPrice(lot));
  if (result <= 0) return '价格未记录';
  if (start <= 0) return '起拍为 0，无溢价率';
  return `溢价率 ${((result - start) / start * 100).toFixed(1)}%`;
}

function historyEndMs(lot: Lot) {
  if (lot.status === 'LOT_STATUS_CANCELLED') return Number(lot.cancelledAtUnixMs || 0);
  if (isSettlementLot(lot)) return Number(lot.settledAtUnixMs || 0);
  return Number(lot.endsAtUnixMs || 0);
}

function actualDurationSeconds(lot: Lot) {
  const start = Number(lot.startedAtUnixMs || 0);
  const end = historyEndMs(lot);
  if (!Number.isFinite(start) || !Number.isFinite(end) || start <= 0 || end <= start) return 0;
  return Math.round((end - start) / 1000);
}

function actualDurationText(lot: Lot) {
  const seconds = actualDurationSeconds(lot);
  return seconds > 0 ? formatDurationText(seconds) : '未记录';
}

type HistoryInsight = { tone: 'success' | 'warning' | 'danger' | 'info'; title: string; detail: string };

function historyInsightItems(lot: Lot): HistoryInsight[] {
  const items: HistoryInsight[] = [];
  const result = moneyAmount(resultPrice(lot));
  const estimate = moneyAmount(lot.estimatePrice);
  const cap = moneyAmount(lot.rule.capPrice);
  const duration = actualDurationSeconds(lot);
  const configured = Number(lot.rule.durationSeconds || 0);

  if (isSettlementLot(lot) && estimate > 0 && result > 0 && result < estimate * 0.9) {
    items.push({ tone: 'warning', title: '低于参考估价', detail: '结果价明显低于资料估价，复盘时优先看开场讲解、起拍价和受众匹配。' });
  }
  if (isSettlementLot(lot) && estimate > 0 && result > estimate * 1.1) {
    items.push({ tone: 'success', title: '高于参考估价', detail: '结果价超过资料估价，后续同类拍品可以保留讲解节奏和规则配置。' });
  }
  if (isSettlementLot(lot) && cap > 0 && result >= cap) {
    items.push({ tone: 'info', title: '触达封顶价', detail: '价格打到封顶，说明热度较高或封顶设置偏保守。' });
  }
  if (duration > 0 && configured > 0 && duration > configured * 1.3) {
    items.push({ tone: 'info', title: '竞拍时长拉长', detail: '实际耗时明显长于配置时长，可能发生多次防狙击延时。' });
  }
  if (lot.status === 'LOT_STATUS_CANCELLED' && !lot.cancelReason) {
    items.push({ tone: 'danger', title: '取消原因缺失', detail: '取消记录没有写明原因，后续团队复盘和责任交接会变困难。' });
  }
  if (lot.status === 'LOT_STATUS_FAILED') {
    items.push({ tone: 'danger', title: '异常拍品', detail: '该拍品进入异常状态，需要运营确认是无人出价、链路问题还是人工处理。' });
  }
  if (!(lot.trustCards || []).length) {
    items.push({ tone: 'warning', title: '缺少讲解卡', detail: '没有沉淀证书、瑕疵、细节或售后讲解，主播复盘时可补齐话术材料。' });
  }
  if (!items.length) {
    items.push({ tone: 'success', title: '复盘信号正常', detail: '现有字段没有显示明显价格、时长或资料缺口。' });
  }
  return items;
}
