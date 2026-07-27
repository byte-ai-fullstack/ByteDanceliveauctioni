import { useState } from 'react';
import { AlertTriangle, Clock3, ListChecks, Package, Radio, RefreshCw, ShieldAlert, Trophy, Wifi } from 'lucide-react';
import { AppLink } from '../../shared/router/AppLink';
import { cancelLot, revealTrustCard, settleLot, startDuel } from '../auction/api/auctionApi';
import { canSettleLot, isLiveLot, isQueueReadyLot, lotStatusLabel, lotStatusTone } from '../../entities/auction/model/auctionStatus';
import type { Bid, Lot, RoomSnapshot } from '../../shared/api/types';
import { resultMessage } from '../../shared/api/result';
import { formatDateTimeText, formatMoneyText } from '../../shared/lib/format';
import { formatAuctionLeftMs, getLotLeftMs, getServerOffsetMs } from '../../shared/lib/time';
import { roomSocketStatusLabel, useRoomSocket } from '../../shared/realtime/useRoomSocket';
import { StudioBadge, StudioButton, StudioCard, StudioEmptyState, StudioMetricCard, StudioPageHeader, StudioTable, StudioTableSkeleton } from '../../pages/host-console/components/studio-ui';
import { useLiveControlQuery, useRoomSnapshotQuery } from './model/useRealtimeQueries';

type LinkEvent = { lotVersion: number; time: string; type: string; lotId?: string; detail: string };

export function LiveControlPage({ roomId }: { roomId: string }) {
  const [realtimeSnapshot, setRealtimeSnapshot] = useState<RoomSnapshot | null>(null);
  const [realtimeLot, setRealtimeLot] = useState<Lot | null>(null);
  const [socketError, setError] = useState('');
  const [working, setWorking] = useState('');
  const { snapshot: httpSnapshot, lots, error: queryError, sync: syncQuery } = useLiveControlQuery(roomId);
  const snapshot = realtimeSnapshot ?? httpSnapshot;
  const lot = realtimeLot ?? snapshot?.currentLot ?? lots.find(isLiveLot) ?? null;
  const error = socketError || queryError;

  const syncRoom = async (): Promise<RoomSnapshot | void> => {
    setError('');
    setRealtimeSnapshot(null);
    setRealtimeLot(null);
    try {
      const data = await syncQuery();
      return data.snapshot;
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
    }
  };

  const socket = useRoomSocket({
    roomId,
    recoverSnapshot: syncRoom,
    onStatusChange: (status) => {
      if (status === 'connected') {
        setError((current) => current.includes('实时连接') ? '' : current);
      }
    },
    onEvent: (event) => {
      if (event.snapshot) {
        setRealtimeSnapshot(event.snapshot);
        setRealtimeLot(event.snapshot.currentLot || null);
      }
      if (event.lot) setRealtimeLot(event.lot as Lot);
    },
    onSnapshot: (nextSnapshot) => {
      setRealtimeSnapshot(nextSnapshot);
      setRealtimeLot(nextSnapshot.currentLot || null);
    },
    onError: (e, phase) => {
      if (phase === 'socket') return;
      setError(resultMessage(e));
    },
  });

  const nextLot = lots.find((item) => isQueueReadyLot(item) && item.id !== lot?.id) || null;
  const wsState = roomSocketStatusLabel(socket.status);

  const action = async (name: string, fn: () => Promise<unknown>) => {
    setWorking(name);
    setError('');
    try {
      await fn();
      await syncRoom();
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
    } finally {
      setWorking('');
    }
  };

  return <section className={`liveControlPage ${lot ? 'isLive' : 'isPrepared'}`}>
    <StudioCard padding="lg" className="controlTopBar">
      <StudioPageHeader eyebrow="Realtime control" title="直播间中控台" actions={<><AppLink className="studioButton studioButton-secondary studioButton-md controlUtilityButton" to="/admin/auctions">返回队列</AppLink><StudioButton type="button" variant="secondary" className="controlUtilityButton" icon={<RefreshCw size={15} />} loading={working === '同步'} onClick={() => void action('同步', syncRoom)}>立即同步</StudioButton></>} />
    </StudioCard>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    <section className="auctionMgmtStats liveControlStatusGrid">
      <StudioMetricCard icon={<Wifi />} label="WebSocket" value={wsState} trend={`重连 ${socket.reconnectCount} 次`} tone={socket.status === 'connected' ? 'success' : 'warning'} />
      <StudioMetricCard icon={<Radio />} label="当前竞拍" value={lot?.id || '无 LIVE'} trend={lot?.title || '等待开拍'} tone={lot ? 'success' : 'warning'} />
      <StudioMetricCard icon={<Trophy />} label="参与 / 出价" value={lot ? `${lot.stats.participantCount} / ${lot.stats.bidCount}` : 0} trend="来自 lot.stats" tone="info" />
      <StudioMetricCard icon={<Clock3 />} label="服务器偏移" value={snapshot?.serverTimeUnixMs ? `${getServerOffsetMs(snapshot.serverTimeUnixMs)}ms` : '待同步'} trend="倒计时以服务端时间校正" tone="purple" />
    </section>
    {!lot ? <PreparedStage nextLot={nextLot} onSync={() => void syncRoom()} /> : <div className="controlRoomGrid">
      <aside className="controlLeftRail hostBriefingRail"><RoomLivePreview lot={lot} snapshot={snapshot} wsState={wsState} /><TrustCardPanel lot={lot} working={working} onReveal={(cardId) => void action('展示讲解卡', () => revealTrustCard(lot.id, cardId))} /></aside>
      <main className="controlCenterRail"><PriceCommandBoard lot={lot} snapshot={snapshot} /><ControlActionDeck lot={lot} working={working} onDuel={() => void action('进入决胜', () => startDuel(lot.id))} onSettle={() => void action('落锤成交', () => settleLot(lot.id))} onCancel={(reason) => void action('异常取消', () => cancelLot(lot.id, reason))} /></main>
      <aside className="controlRightRail"><RealtimeBidFeedPanel bids={snapshot?.recentBids || []} /><LiveRankingBoard ranking={snapshot?.ranking || []} leadingUserId={lot.leadingUserId} /></aside>
    </div>}
    <NextLotQueue lot={nextLot} />
  </section>;
}

export function BidAuditPage({ roomId }: { roomId: string }) {
  const [realtimeSnapshot, setRealtimeSnapshot] = useState<RoomSnapshot | null>(null);
  const [socketError, setError] = useState('');
  const { snapshot: httpSnapshot, loading, error: queryError, sync: syncQuery } = useRoomSnapshotQuery(roomId);
  const snapshot = realtimeSnapshot ?? httpSnapshot;
  const error = socketError || queryError;

  const sync = async (): Promise<RoomSnapshot | void> => {
    setError('');
    setRealtimeSnapshot(null);
    try {
      return await syncQuery();
    } catch (e) {
      setError(resultMessage(e));
    }
  };

  useRoomSocket({
    roomId,
    recoverSnapshot: sync,
    onSnapshot: setRealtimeSnapshot,
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

  const bids = snapshot?.recentBids || [];
  const leadingUserId = snapshot?.currentLot?.leadingUserId || snapshot?.ranking?.[0]?.userId || '';
  return <section className="postLivePage bidAuditPage">
    <StudioCard padding="lg" className="postLiveHeader bidAuditHero"><StudioPageHeader eyebrow="Bid audit" title="出价明细" actions={<StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} loading={loading} onClick={() => void sync()}>重新同步</StudioButton>} /></StudioCard>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    <section className="postLiveMetricGrid bidMetricGrid"><StudioMetricCard icon={<ListChecks />} label="最近出价" value={bids.length} trend="recentBids" tone="info" /><StudioMetricCard icon={<Trophy />} label="当前领先" value={leadingUserId ? 1 : 0} trend={leadingUserId || '等待出价'} tone="success" /><StudioMetricCard icon={<ShieldAlert />} label="拒绝明细" value="待接口" trend="后端未提供拒绝列表" tone="danger" /></section>
    {loading ? <StudioTableSkeleton rows={6} columns={7} /> : <StudioTable className="bidAuditTable" rows={bids} rowKey={(bid) => bid.id} header={`共 ${bids.length} 条最近出价`} empty={<StudioEmptyState icon={<ListChecks size={34} />} title="暂无出价明细" description="房间快照当前没有 recentBids。" compact />} columns={[{ label: '拍品 / 用户', render: (bid) => <div className="bidIdentityCell"><b>{bid.lotId}</b><span>{bid.nickname || bid.userId}</span><small>accepted bid</small></div> }, { label: '出价金额', render: (bid) => <strong className="moneyText">{formatMoneyText(bid.amount)}</strong> }, { label: '领先', render: (bid) => <StudioBadge tone={bid.userId === leadingUserId ? 'success' : 'neutral'}>{bid.userId === leadingUserId ? '领先' : '未领先'}</StudioBadge> }, { label: '校验结果', render: () => <StudioBadge tone="success">有效</StudioBadge> }, { label: '延迟', render: () => <span className="latencyText">待后端字段</span> }, { label: '幂等 Key', render: () => <code>后端未返回</code> }, { label: '出价时间', render: (bid) => formatDateTimeText(bid.createdAtUnixMs) }]} />}
  </section>;
}

export function RealtimeDiagnosticsPage({ roomId }: { roomId: string }) {
  const [realtimeSnapshot, setRealtimeSnapshot] = useState<RoomSnapshot | null>(null);
  const [events, setEvents] = useState<LinkEvent[]>([]);
  const [lastEventType, setLastEventType] = useState('');
  const [lastHeartbeat, setLastHeartbeat] = useState('');
  const [socketError, setError] = useState('');
  const { snapshot: httpSnapshot, error: queryError, dataUpdatedAt, sync: syncQuery } = useRoomSnapshotQuery(roomId);
  const snapshot = realtimeSnapshot ?? httpSnapshot;
  const error = socketError || queryError;
  const heartbeatText = lastHeartbeat || (snapshot && dataUpdatedAt ? timeText(dataUpdatedAt) : '未收到');
  const eventTypeText = lastEventType || (snapshot ? 'ROOM_SNAPSHOT' : '暂无');

  const sync = async (): Promise<RoomSnapshot | void> => {
    setError('');
    setRealtimeSnapshot(null);
    setLastHeartbeat('');
    setLastEventType('');
    try {
      return await syncQuery();
    } catch (e) {
      setError(resultMessage(e));
    }
  };

  const socket = useRoomSocket({
    roomId,
    recoverSnapshot: sync,
    onEvent: (event, meta) => {
      setLastHeartbeat(meta.receivedAtText);
      setLastEventType(event.type);
      setEvents((current) => [{ lotVersion: meta.lotVersion, time: meta.receivedAtText, type: event.type, lotId: event.lotId, detail: event.reason || event.lot?.title || event.bid?.nickname || '房间事件' }, ...current].slice(0, 80));
      if (event.snapshot) setRealtimeSnapshot(event.snapshot);
    },
    onSnapshot: setRealtimeSnapshot,
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

  const wsState = roomSocketStatusLabel(socket.status);
  return <section className="realtimeDiagPage">
    <StudioCard padding="lg" className="realtimeDiagHero"><StudioPageHeader eyebrow="Realtime diagnostics" title="实时链路诊断" description="诊断当前主播空间唯一直播间的 WebSocket、快照恢复、服务端时间偏移与事件流。" actions={<StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} onClick={() => void sync()}>重新同步房间快照</StudioButton>} /></StudioCard>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    <section className="realtimeSyncCapsule"><div><Wifi size={18} /><span>实时同步状态</span><StudioBadge tone={socket.status === 'connected' ? 'success' : 'warning'}>{wsState}</StudioBadge></div><div className="syncCapsuleMetrics"><span>最近心跳：<b>{heartbeatText}</b></span><span>重连次数：<b>{socket.reconnectCount}</b></span><span>服务器偏移：<b>{snapshot?.serverTimeUnixMs ? `${getServerOffsetMs(snapshot.serverTimeUnixMs)}ms` : '待同步'}</b></span><span>最近事件：<b>{eventTypeText}</b></span></div></section>
    <section className="realtimeDiagGrid"><StudioCard title="客户端可计算指标" subtitle="Local diagnostics"><div className="systemHealthGrid"><span>快照版本：<b>{snapshot?.currentLot?.version || '待同步'}</b></span><span>排行榜人数：<b>{snapshot?.ranking?.length || 0}</b></span><span>当前竞拍：<b>{snapshot?.currentLot?.id || '无 LIVE'}</b></span><span>事件数：<b>{events.length}</b></span></div></StudioCard><StudioCard title="最近事件流" subtitle="Events"><div className="linkEventList inline">{events.length ? events.map((event) => <div key={`${event.lotVersion}-${event.time}`}><b>v{event.lotVersion}</b><span>{event.type}</span><small>{event.time} · {event.lotId || roomId}</small><p>{event.detail}</p></div>) : <p>暂无事件。等待 WebSocket 事件或手动同步房间快照。</p>}</div></StudioCard></section>
  </section>;
}

function timeText(value: number) {
  return new Date(value).toLocaleTimeString('zh-CN', { hour12: false });
}

function PreparedStage({ nextLot, onSync }: { nextLot: Lot | null; onSync: () => void }) {
  return <section className="preparedControlStage"><StudioCard title="当前无 LIVE" subtitle="Ready"><StudioEmptyState icon={<Radio size={30} />} title="等待开拍" description={nextLot ? `下一件拍品：${nextLot.title}` : '队列中没有待开拍拍品。'} action={<><AppLink className="studioButton studioButton-primary studioButton-md" to="/admin/auctions">查看本场队列</AppLink><StudioButton type="button" variant="secondary" onClick={onSync}>同步房间状态</StudioButton></>} /></StudioCard></section>;
}

function RoomLivePreview({ lot, snapshot, wsState }: { lot: Lot; snapshot: RoomSnapshot | null; wsState: string }) {
  return <section className="roomLivePreview"><div className="liveFrame"><img src={lot.imageUrl || '/vite.svg'} alt={lot.title} /><span>{lotStatusLabel(lot.status)}</span><b>{formatMoneyText(lot.currentPrice)}</b></div><div><span>排名 {snapshot?.ranking?.length || 0}</span><span>同步 {wsState}</span><span>房间隔离 ON</span></div></section>;
}

function TrustCardPanel({ lot, working, onReveal }: { lot: Lot; working: string; onReveal: (cardId: string) => void }) {
  const cards = lot.trustCards || [];
  return <section className="controlCard"><header><h3>讲解卡 / 信任信息</h3></header><div className="trustCardList">{cards.length ? cards.map((card) => <div key={card.id} className={card.revealed ? 'revealed' : ''}><b>{card.title}</b><span>{card.type.replace('TRUST_CARD_TYPE_', '')}</span><p>{card.content}</p><small>{card.revealed ? '已展示给观众' : '未展示'}</small><button type="button" disabled={card.revealed || Boolean(working)} onClick={() => onReveal(card.id)}>{card.revealed ? '已展示' : '展示给观众'}</button></div>) : <p>讲解卡待补充。</p>}</div></section>;
}

function PriceCommandBoard({ lot, snapshot }: { lot: Lot; snapshot: RoomSnapshot | null }) {
  const nextPrice = { ...lot.currentPrice, amount: Number(lot.currentPrice?.amount || 0) + Number(lot.rule.minIncrement?.amount || 0) };
  return <section className="priceCommandBoard auctionCommandCenter"><div className="priceCommandEyebrow"><span>Command Area</span><StudioBadge tone={lotStatusTone(lot.status)}>{lotStatusLabel(lot.status)}</StudioBadge></div><div className="currentPriceFocus"><p>当前最高价</p><strong className="livePriceBig">{formatMoneyText(lot.currentPrice)}</strong><small>领先用户：{lot.leadingNickname || snapshot?.ranking?.[0]?.nickname || '暂无'}</small></div><span className="serverCountdown"><Clock3 size={22} />{formatAuctionLeftMs(getLotLeftMs(lot, snapshot?.serverTimeUnixMs), 'control')}</span><div className="priceCommandMetrics"><span>下一口价：<b>{formatMoneyText(nextPrice)}</b></span><span>参与人数：<b>{snapshot?.ranking?.length || 0}</b></span><span>出价次数：<b>{snapshot?.recentBids?.length || 0}</b></span><span>规则版本：<b>v{lot.version || 1}</b></span></div></section>;
}

function ControlActionDeck({ lot, working, onDuel, onSettle, onCancel }: { lot: Lot; working: string; onDuel: () => void; onSettle: () => void; onCancel: (reason: string) => void }) {
  const disabled = Boolean(working);
  const settleReady = canSettleLot(lot);
  const [cancelReason, setCancelReason] = useState('');
  const trimmedReason = cancelReason.trim();
  return <section className="controlActionDeck"><header><h3>控场操作</h3><StudioBadge tone={lotStatusTone(lot.status)}>{lotStatusLabel(lot.status)}</StudioBadge></header><div className="controlActionsGrid"><button type="button" className="controlActionButton controlActionButton-duel" disabled={disabled} onClick={onDuel}>进入决胜</button><button type="button" className="controlActionButton controlActionButton-muted" disabled>推送提醒待接口</button><button type="button" className="controlActionButton controlActionButton-muted" disabled>延时待接口</button></div><div className="dangerActionStrip"><button type="button" className="settleButton" disabled={disabled || !settleReady} title={settleReady ? '落锤成交' : '暂无有效出价，不能落锤成交'} onClick={onSettle}>{settleReady ? '落锤成交' : '等待出价'}</button><input value={cancelReason} onChange={(event) => setCancelReason(event.target.value)} placeholder="异常取消原因" disabled={disabled} /><button type="button" className="cancelButton" disabled={disabled || !trimmedReason} onClick={() => onCancel(trimmedReason)}>异常取消</button></div></section>;
}

function RealtimeBidFeedPanel({ bids }: { bids: Bid[] }) {
  return <section className="controlSideCard"><h3>实时出价流</h3><div className="controlBidFeed">{bids.length ? bids.slice(0, 12).map((bid) => <div key={bid.id}><span>{bid.nickname || bid.userId}</span><b>{formatMoneyText(bid.amount)}</b><small>有效 · {formatDateTimeText(bid.createdAtUnixMs)}</small></div>) : <StudioEmptyState compact icon={<ListChecks size={24} />} title="等待实时出价事件" description="开拍后这里会显示服务端接受的最新出价。" />}</div></section>;
}

function LiveRankingBoard({ ranking, leadingUserId }: { ranking: RoomSnapshot['ranking']; leadingUserId: string }) {
  const top = ranking[0];
  const topAmount = Number(top?.amount?.amount || 0);
  return <section className="controlSideCard rankingPanel"><h3>实时排行榜 TOP 10</h3><div className="controlRanking">{ranking.length ? ranking.slice(0, 10).map((item) => <div key={item.userId}><b>#{item.rank}</b><span>{item.nickname || item.userId}</span><strong>{formatMoneyText(item.amount)}</strong><small>{item.userId === leadingUserId ? '领先' : `差距 ${formatMoneyText({ amount: Math.max(0, topAmount - Number(item.amount.amount || 0)), currency: item.amount.currency || 'CNY' })}`}</small></div>) : <StudioEmptyState compact icon={<Trophy size={24} />} title="排行榜等待 snapshot" description="房间快照恢复后展示 TOP 10 排名。" />}</div></section>;
}

function NextLotQueue({ lot }: { lot: Lot | null }) {
  return <section className="controlBottomCard"><h3>下一场待开拍</h3>{lot ? <div className="nextQueueItem"><b>{lot.title}</b><span>起拍 {formatMoneyText(lot.rule.startPrice)} · 加价 {formatMoneyText(lot.rule.minIncrement)}</span><AppLink className="studioButton studioButton-secondary studioButton-sm" to="/admin/auctions">回队列开拍</AppLink></div> : <StudioEmptyState compact icon={<Package size={24} />} title="暂无下一场待开拍" description="可以回到本场队列调整顺序，或添加新拍品。" />}</section>;
}
