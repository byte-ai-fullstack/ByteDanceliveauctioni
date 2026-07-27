import { useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent, type ReactNode } from 'react';
import { ClipboardList, MoreHorizontal, Search, ShoppingCart } from 'lucide-react';
import { LOT_STATUS, type Lot } from '../../../shared/api/types';
import { getServerNowMs } from '../../../shared/lib/time';
import type { AuctionPanelTab, LiveRoomController } from '../hooks/useLiveRoomController';
import { ShopOrdersContent } from '../../../pages/ShopOrdersPage';
import { BidPanel } from '../../auction-bid/components/BidPanel';
import { BuyerAuthPanel } from './BuyerAuthPanel';
import { CurrentLotCard } from './CurrentLotCard';
import { LotQueueList } from './LotQueueList';
import { deriveLotDisplayState, orderForLot } from '../model/lotDisplayState';

type SheetMode = 'detail' | 'bid';
type DrawerDragState = {
  pointerId: number;
  startY: number;
  startHeight: number;
  lastHeight: number;
};

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function DrawerToolButton({
  active = false,
  icon,
  label,
  onClick,
}: {
  active?: boolean;
  icon: ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button type="button" className={`auctionDrawerToolButton${active ? ' active' : ''}`} onClick={onClick}>
      {icon}
      <span>{label}</span>
    </button>
  );
}

function CurrentAuctionPanel({
  controller,
  onBidSheetOpenChange,
  onCloseBidSheet,
  onOpenLotDetail,
  onCloseAuctionPanel,
}: {
  controller: LiveRoomController;
  onBidSheetOpenChange: (open: boolean) => void;
  onCloseBidSheet: () => void;
  onOpenLotDetail?: (lot: Lot) => void;
  onCloseAuctionPanel: () => void;
}) {
  const {
    room,
    auctionPanel,
    loading,
    error,
    currentLot,
    meId,
    accountRoleMessage,
    bidAuthPanelOpen,
    buyerAuth,
    actions,
  } = controller;
  const [sheetLotId, setSheetLotId] = useState('');
  const [sheetMode, setSheetMode] = useState<SheetMode>('detail');
  const displayNowMs = getServerNowMs(room.serverTimeUnixMs, room.serverTimeReceivedAtUnixMs);
  const lots = useMemo(() => {
    if (auctionPanel.lots.length) {
      if (currentLot && !auctionPanel.lots.some((lot) => lot.id === currentLot.id)) return [currentLot, ...auctionPanel.lots];
      if (currentLot) return auctionPanel.lots.map((lot) => lot.id === currentLot.id ? currentLot : lot);
      return auctionPanel.lots;
    }
    return currentLot ? [currentLot] : [];
  }, [auctionPanel.lots, currentLot]);
  const sheetLot = lots.find((lot) => lot.id === sheetLotId) || null;
  const sheetIsCurrent = Boolean(sheetLot && currentLot && sheetLot.id === currentLot.id);
  const sheetDisplayState = sheetLot ? deriveLotDisplayState(sheetLot, {
    order: orderForLot(room.orders, sheetLot),
    paymentKnownPaid: Boolean(room.paidLotIds[sheetLot.id]),
    nowMs: displayNowMs,
  }) : undefined;
  const sheetCanBid = Boolean(sheetLot && sheetIsCurrent && sheetDisplayState === 'live');
  const lotDisplayState = (lot: Lot) => deriveLotDisplayState(lot, {
    order: orderForLot(room.orders, lot),
    paymentKnownPaid: Boolean(room.paidLotIds[lot.id]),
    nowMs: displayNowMs,
  });
  const canBidLot = (lot: Lot) => lotDisplayState(lot) === 'live' && currentLot?.id === lot.id;
  const openLotSheet = (lot: Lot) => {
    if (onOpenLotDetail) {
      onCloseAuctionPanel();
      onOpenLotDetail(lot);
      return;
    }

    const nextMode: SheetMode = canBidLot(lot) ? 'bid' : 'detail';
    setSheetLotId(lot.id);
    setSheetMode(nextMode);
    onBidSheetOpenChange(nextMode === 'bid');
  };
  const handleQueuePrimaryAction = (lot: Lot) => {
    const displayState = lotDisplayState(lot);

    if (displayState === 'live' && currentLot?.id === lot.id) {
      setSheetLotId(lot.id);
      setSheetMode('bid');
      onBidSheetOpenChange(true);
      return;
    }

    openLotSheet(lot);
    if (displayState === 'live' && currentLot?.id !== lot.id) actions.showNotice('请等待主播切到该拍品后再出价');
  };
  const sheetInBidMode = sheetMode === 'bid' && sheetCanBid;
  const closeSheet = () => {
    if (sheetInBidMode) {
      onCloseBidSheet();
      return;
    }
    setSheetLotId('');
    setSheetMode('detail');
    onBidSheetOpenChange(false);
  };

  useEffect(() => {
    if (currentLot?.status !== LOT_STATUS.CANCELLED) return undefined;
    const timer = window.setTimeout(() => {
      setSheetLotId('');
      setSheetMode('detail');
      onBidSheetOpenChange(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [currentLot?.id, currentLot?.status, onBidSheetOpenChange]);

  useEffect(() => {
    if (sheetMode === 'bid' && !sheetCanBid) onBidSheetOpenChange(false);
  }, [sheetCanBid, sheetMode, onBidSheetOpenChange]);

  return (
    <section className="currentAuctionPanel">
      {accountRoleMessage ? <section className="emptyState error">{accountRoleMessage}</section> : null}
      {loading ? <section className="drawerEmpty">正在进入直播间...</section> : null}
      {error ? <section className="emptyState error">{error}</section> : null}
      {!sheetInBidMode ? (
        <LotQueueList
          lots={lots}
          currentLotId={currentLot?.id}
          selectedLotId={sheetLot?.id || currentLot?.id}
          loading={auctionPanel.loading}
          error={auctionPanel.error}
          onRefresh={() => void actions.refreshRoomLots().catch(() => undefined)}
          onSelectLot={openLotSheet}
          onPrimaryAction={handleQueuePrimaryAction}
          onPayOrder={actions.setPayOrder}
          orders={room.orders}
          paidLotIds={room.paidLotIds}
          meId={meId}
          nowMs={displayNowMs}
        />
      ) : null}

      {bidAuthPanelOpen ? (
        <section className={`liveBidSheet liveProductSheet liveAuthSheet${sheetInBidMode ? ' liveProductSheetStandalone' : ''}`} aria-label="登录后出价">
          <button type="button" className="bidSheetClose" onClick={actions.closeBuyerAuthPanel} aria-label="关闭登录窗口">×</button>
          <BuyerAuthPanel auth={buyerAuth} />
        </section>
      ) : sheetLot ? (
        <section className={`liveBidSheet liveProductSheet${sheetInBidMode ? ' liveProductSheetStandalone' : ''}`} aria-label={sheetInBidMode ? '出价面板' : '商品详情'}>
          <button
            type="button"
            className="bidSheetClose"
            onClick={closeSheet}
            aria-label={sheetInBidMode ? '关闭出价面板' : '收起商品详情'}
          >
            ×
          </button>
          <CurrentLotCard
            lot={sheetLot}
            serverTimeUnixMs={room.serverTimeUnixMs}
            serverTimeReceivedAtUnixMs={room.serverTimeReceivedAtUnixMs}
            displayState={sheetDisplayState}
          />
          {sheetCanBid ? (
            <BidPanel
              lot={sheetLot}
              loading={controller.isBidPending}
              disabledReason={accountRoleMessage || ''}
              onBid={actions.submitBid}
              onTip={actions.showNotice}
            />
          ) : null}
        </section>
      ) : null}

      {!loading && !currentLot && !lots.length ? <section className="drawerEmpty">当前暂无商品，等待主播上架</section> : null}

      {room.localOptimistic.pendingBid ? (
        <p className="pendingHint">
          订单状态同步中，请稍候...
        </p>
      ) : null}

      {currentLot?.status === LOT_STATUS.CANCELLED ? <div className="cancelBanner">{currentLot.cancelReason ? `本件拍品已由主播取消，原因：${currentLot.cancelReason}` : '本件拍品已由主播取消'}</div> : null}
    </section>
  );
}

type AuctionDrawerProps = {
  controller: LiveRoomController;
  onOpenLotDetail?: (lot: Lot) => void;
};

export function AuctionDrawer(props: AuctionDrawerProps) {
  if (!props.controller.auctionPanel.open) return null;
  return <OpenAuctionDrawer {...props} />;
}

function OpenAuctionDrawer({
  controller,
  onOpenLotDetail,
}: AuctionDrawerProps) {
  const { auctionPanel, actions } = controller;
  const [bidSheetOpen, setBidSheetOpen] = useState(false);
  const [drawerHeight, setDrawerHeight] = useState<number | null>(null);
  const [drawerExpanded, setDrawerExpanded] = useState(false);
  const drawerRef = useRef<HTMLElement | null>(null);
  const dragStateRef = useRef<DrawerDragState | null>(null);
  const windowDragCleanupRef = useRef<(() => void) | null>(null);

  useEffect(() => () => {
    windowDragCleanupRef.current?.();
    windowDragCleanupRef.current = null;
  }, []);

  const activeTab = auctionPanel.tab === 'mine' ? 'mine' : 'current';
  const drawerStyle = drawerHeight ? ({ '--auction-drawer-height': `${drawerHeight}px` } as CSSProperties) : undefined;
  const drawerClassName = [
    'auctionDrawer',
    bidSheetOpen ? 'auctionDrawerBidSheet' : '',
    activeTab === 'mine' ? 'auctionDrawerOrdersSheet' : '',
    drawerExpanded ? 'auctionDrawerExpanded' : '',
  ].filter(Boolean).join(' ');

  const closeAuctionPanel = () => {
    setBidSheetOpen(false);
    actions.closeAuctionPanel();
  };

  const selectTab = (tab: AuctionPanelTab) => {
    setBidSheetOpen(false);
    actions.setAuctionPanelTab(tab);
    if (tab === 'mine') void actions.refreshOrders().catch(() => undefined);
  };

  const measureDrawerBounds = () => {
    const viewportHeight = typeof window === 'undefined' ? 844 : window.innerHeight || 844;
    const rootHeight = drawerRef.current?.parentElement?.getBoundingClientRect().height || viewportHeight;
    const compactHeight = Math.min(rootHeight * 0.78, 760);
    const minHeight = clamp(compactHeight, Math.min(430, rootHeight - 34), Math.max(430, rootHeight - 34));
    const lowerHeight = Math.max(170, Math.min(minHeight - 1, rootHeight * 0.34));
    return {
      lower: lowerHeight,
      min: minHeight,
      max: Math.max(minHeight, rootHeight),
    };
  };

  const currentDrawerHeight = () => {
    const { min, max } = measureDrawerBounds();
    return drawerRef.current?.getBoundingClientRect().height || (drawerExpanded ? max : min);
  };

  const updateDrawerDrag = (clientY: number) => {
    const drag = dragStateRef.current;
    if (!drag) return;
    const { lower, min, max } = measureDrawerBounds();
    const nextHeight = clamp(drag.startHeight + drag.startY - clientY, lower, max);
    drag.lastHeight = nextHeight;
    setDrawerHeight(nextHeight);
    setDrawerExpanded(nextHeight > min + (max - min) * 0.42);
  };

  const finishDrawerDragAt = () => {
    const drag = dragStateRef.current;
    if (!drag) return;
    const { min, max } = measureDrawerBounds();
    const shouldExpand = drag.lastHeight > min + (max - min) * 0.42;
    dragStateRef.current = null;
    windowDragCleanupRef.current?.();
    windowDragCleanupRef.current = null;
    setDrawerHeight(shouldExpand ? max : null);
    setDrawerExpanded(shouldExpand);
    if (drag.lastHeight < min) {
      if (activeTab === 'mine') {
        selectTab('current');
      } else {
        closeAuctionPanel();
      }
    }
  };

  const handleDragStart = (event: PointerEvent<HTMLElement>) => {
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    event.preventDefault();
    event.stopPropagation();
    const startHeight = currentDrawerHeight();
    dragStateRef.current = {
      pointerId: event.pointerId,
      startY: event.clientY,
      startHeight,
      lastHeight: startHeight,
    };
    setDrawerHeight(startHeight);
    try {
      event.currentTarget.setPointerCapture?.(event.pointerId);
    } catch {
      // Window listeners below keep dragging responsive when pointer capture is unavailable.
    }
    const handleWindowMove = (moveEvent: globalThis.PointerEvent) => {
      if (moveEvent.pointerId !== event.pointerId) return;
      moveEvent.preventDefault();
      updateDrawerDrag(moveEvent.clientY);
    };
    const handleWindowEnd = (endEvent: globalThis.PointerEvent) => {
      if (endEvent.pointerId !== event.pointerId) return;
      finishDrawerDragAt();
    };
    windowDragCleanupRef.current?.();
    window.addEventListener('pointermove', handleWindowMove, { passive: false });
    window.addEventListener('pointerup', handleWindowEnd);
    window.addEventListener('pointercancel', handleWindowEnd);
    windowDragCleanupRef.current = () => {
      window.removeEventListener('pointermove', handleWindowMove);
      window.removeEventListener('pointerup', handleWindowEnd);
      window.removeEventListener('pointercancel', handleWindowEnd);
    };
  };

  return (
    <div className="auctionDrawerMask" onClick={closeAuctionPanel}>
      <section ref={drawerRef} className={drawerClassName} style={drawerStyle} role="dialog" aria-modal="true" aria-label={activeTab === 'mine' ? '我的订单' : '直播商品'} onClick={(event) => event.stopPropagation()}>
        {!bidSheetOpen ? (
          <button type="button" className="auctionDrawerDragHandle" aria-label="拖动面板" onPointerDown={handleDragStart} />
        ) : null}
        {!bidSheetOpen && activeTab === 'current' ? (
          <>
            <header className="auctionDrawerHeader">
              <button
                type="button"
                className="auctionDrawerSearch"
                onClick={() => actions.showNotice('商品搜索暂未开放')}
              >
                <Search size={23} />
                <span>搜索商品/序号</span>
              </button>
              <div className="auctionDrawerTools" aria-label="橱窗快捷入口">
                <DrawerToolButton icon={<ClipboardList size={25} />} label="订单" onClick={() => selectTab('mine')} />
                <DrawerToolButton icon={<ShoppingCart size={26} />} label="购物车" onClick={() => actions.showNotice('购物车暂未开放')} />
                <DrawerToolButton icon={<MoreHorizontal size={27} />} label="更多" onClick={() => actions.showNotice('更多橱窗工具暂未开放')} />
              </div>
            </header>
          </>
        ) : null}

        <div className="drawerContent">
          {activeTab === 'current' ? (
            <CurrentAuctionPanel
              controller={controller}
              onBidSheetOpenChange={setBidSheetOpen}
              onCloseBidSheet={closeAuctionPanel}
              onOpenLotDetail={onOpenLotDetail}
              onCloseAuctionPanel={closeAuctionPanel}
            />
          ) : null}
          {activeTab === 'mine' ? <ShopOrdersContent embedded initialFrom="room" onBack={() => selectTab('current')} onSheetDragStart={handleDragStart} /> : null}
        </div>
      </section>
    </div>
  );
}
