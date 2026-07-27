import { useEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent as ReactMouseEvent, type PointerEvent as ReactPointerEvent } from 'react';
import { Heart, MessageCircle, MoreHorizontal, Search, ShoppingCart, Store, X } from 'lucide-react';
import { isBiddableLotStatus } from '../../../entities/auction/model/status';
import { formatLotDeposit } from '../../../entities/auction/model/deposit';
import { TRUST_CARD_TYPE, type Lot, type TrustRevealCard } from '../../../shared/api/types';
import { gsap, useGSAP } from '../../../shared/animation/gsap';
import { formatMoney, moneyNumber } from '../../../shared/lib/money';
import { formatEventTime, getServerNowMs } from '../../../shared/lib/time';
import { BidPanel } from '../../auction-bid/components/BidPanel';
import type { LiveRoomController } from '../hooks/useLiveRoomController';
import { deriveLotDisplayState, orderForLot, type LotDisplayState } from '../model/lotDisplayState';
import { BuyerAuthPanel } from './BuyerAuthPanel';
import { CurrentLotCard } from './CurrentLotCard';

type DetailTab = 'product' | 'reviews' | 'detail' | 'recommend';
type DetailSheetMode = 'compact' | 'expanded';

const DETAIL_TABS: Array<{ id: DetailTab; label: string }> = [
  { id: 'product', label: '商品' },
  { id: 'reviews', label: '评价' },
  { id: 'detail', label: '详情' },
  { id: 'recommend', label: '推荐' },
];

function displayStateLabel(state: LotDisplayState): string {
  if (state === 'live') return '正在拍';
  if (state === 'upcoming' || state === 'syncing') return '待开拍';
  if (state === 'pendingPayment') return '截拍中';
  if (state === 'cancelled') return '已取消';
  if (state === 'failed') return '未成交';
  return '已结束';
}

function actionForState(state: LotDisplayState, isCurrentLot: boolean, lot: Lot) {
  if (state === 'live' && isCurrentLot && isBiddableLotStatus(lot.status)) {
    return { label: '去出价', disabled: false, tip: '' };
  }
  if (state === 'live') return { label: '等待主播切到该拍品', disabled: true, tip: '请等待主播切到该拍品后再出价' };
  if (state === 'upcoming' || state === 'syncing') return { label: '等待开拍', disabled: true, tip: '当前拍品还未开拍' };
  if (state === 'pendingPayment') return { label: '截拍中', disabled: true, tip: '当前拍品正在截拍确认' };
  if (state === 'cancelled') return { label: '已取消', disabled: true, tip: '本件拍品已由主播取消' };
  if (state === 'failed') return { label: '竞拍未成交', disabled: true, tip: '本件拍品未成交' };
  return { label: '拍卖已结束', disabled: true, tip: '当前拍品已结束' };
}

function trustCardLabel(card: TrustRevealCard): string {
  if (card.type === TRUST_CARD_TYPE.CERTIFICATE) return '鉴定证书';
  if (card.type === TRUST_CARD_TYPE.FLAW) return '瑕疵说明';
  if (card.type === TRUST_CARD_TYPE.SERVICE) return '服务保障';
  if (card.type === TRUST_CARD_TYPE.PRICE_REF) return '价格参考';
  if (card.type === TRUST_CARD_TYPE.DETAIL) return '商品细节';
  return card.title || '拍品说明';
}

function hasTrustCardContent(card: TrustRevealCard): boolean {
  return Boolean(card.title?.trim() || card.content?.trim() || card.imageUrl?.trim());
}

function currentPrice(lot: Lot): Parameters<typeof formatMoney>[0] {
  return moneyNumber(lot.currentPrice) > 0 ? lot.currentPrice : lot.rule.startPrice;
}

function galleryImagesForLot(lot: Lot): string[] {
  const seen = new Set<string>();
  return [lot.imageUrl, ...(lot.galleryImageUrls || [])]
    .map((url) => url?.trim() || '')
    .filter((url) => {
      if (!url || seen.has(url)) return false;
      seen.add(url);
      return true;
    });
}

function majorPriceText(value: Parameters<typeof formatMoney>[0]): string {
  return formatMoney(value).replace('元', '').replace(/\.00$/, '');
}

function sectionTitleId(tab: DetailTab) {
  return `live-product-detail-${tab}`;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

type LiveProductDetailOverlayProps = {
  controller: LiveRoomController;
  lot: Lot;
  liveSource: string;
  onClose: () => void;
  onSelectLot: (lot: Lot) => void;
};

export function LiveProductDetailOverlay(props: LiveProductDetailOverlayProps) {
  return <LiveProductDetailOverlayContent key={props.lot.id} {...props} />;
}

function LiveProductDetailOverlayContent({
  controller,
  lot,
  liveSource,
  onClose,
  onSelectLot,
}: LiveProductDetailOverlayProps) {
  const rootRef = useRef<HTMLElement | null>(null);
  const sheetRef = useRef<HTMLElement | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const miniRef = useRef<HTMLButtonElement | null>(null);
  const miniVideoRef = useRef<HTMLVideoElement | null>(null);
  const dragStateRef = useRef<{
    pointerId: number;
    startY: number;
    startHeight: number;
    lastHeight: number;
    mode: DetailSheetMode;
  } | null>(null);
  const miniDragStateRef = useRef<{
    pointerId: number;
    startClientX: number;
    startClientY: number;
    startOffsetX: number;
    startOffsetY: number;
    startRect: DOMRect;
    boundsRect: DOMRect;
  } | null>(null);
  const heroDragStateRef = useRef<{
    pointerId: number;
    startClientX: number;
    startClientY: number;
    width: number;
    mode: 'pending' | 'carousel' | 'sheet';
  } | null>(null);
  const windowDragCleanupRef = useRef<(() => void) | null>(null);
  const miniDragCleanupRef = useRef<(() => void) | null>(null);
  const miniDragMovedRef = useRef(false);
  const [activeTab, setActiveTab] = useState<DetailTab>('product');
  const [sheetMode, setSheetMode] = useState<DetailSheetMode>('compact');
  const [sheetHeight, setSheetHeight] = useState<number | null>(null);
  const [miniOffset, setMiniOffset] = useState({ x: 0, y: 0 });
  const [miniDragging, setMiniDragging] = useState(false);
  const [heroImageIndex, setHeroImageIndex] = useState(0);
  const [heroDragOffset, setHeroDragOffset] = useState(0);
  const [searchHeaderProgress, setSearchHeaderProgress] = useState(0);
  const [bidSheetOpen, setBidSheetOpen] = useState(false);
  const [bidSheetTip, setBidSheetTip] = useState('');
  const [bidSheetBidAttempted, setBidSheetBidAttempted] = useState(false);
  const {
    accountRoleMessage,
    anchorName,
    auctionPanel,
    bidAuthPanelOpen,
    buyerAuth,
    currentLot,
    room,
    roomName,
    wsState,
    actions,
  } = controller;
  const nowMs = getServerNowMs(room.serverTimeUnixMs, room.serverTimeReceivedAtUnixMs);
  const displayState = deriveLotDisplayState(lot, {
    order: orderForLot(room.orders, lot),
    paymentKnownPaid: Boolean(room.paidLotIds[lot.id]),
    nowMs,
  });
  const isCurrentLot = currentLot?.id === lot.id;
  const action = actionForState(displayState, isCurrentLot, lot);
  const price = currentPrice(lot);
  const acceptedBids = room.recentBids
    .filter((bid) => bid.accepted !== false && (!bid.lotId || bid.lotId === lot.id))
    .slice(0, 3);
  const recommendations = useMemo(
    () => auctionPanel.lots.filter((item) => item.id !== lot.id).slice(0, 4),
    [auctionPanel.lots, lot.id],
  );
  const galleryImages = useMemo(() => galleryImagesForLot(lot), [lot]);
  const activeHeroImageIndex = clamp(heroImageIndex, 0, Math.max(galleryImages.length - 1, 0));
  const activeHeroImage = galleryImages[activeHeroImageIndex] || lot.imageUrl || '';
  const trustCards = lot.trustCards?.filter((card) => card.revealed !== false || hasTrustCardContent(card)) || [];
  const effectiveSearchProgress = sheetMode === 'expanded' ? searchHeaderProgress : 0;
  const galleryTrackStyle = {
    transform: `translate3d(calc(${-activeHeroImageIndex * 100}% + ${Math.round(heroDragOffset)}px), 0, 0)`,
    transition: heroDragOffset ? 'none' : undefined,
  } as CSSProperties;
  const rootStyle = {
    ...(sheetHeight === null ? {} : { '--live-product-detail-sheet-height': `${Math.round(sheetHeight)}px` }),
    '--live-product-detail-mini-x': `${Math.round(miniOffset.x)}px`,
    '--live-product-detail-mini-y': `${Math.round(miniOffset.y)}px`,
    '--live-product-detail-search-opacity': effectiveSearchProgress.toFixed(3),
    '--live-product-detail-hero-chrome-opacity': (sheetMode === 'expanded' ? 1 - effectiveSearchProgress : 0).toFixed(3),
    '--live-product-detail-compact-close-opacity': (sheetMode === 'compact' ? 1 : 0).toFixed(3),
  } as CSSProperties;

  useEffect(() => () => {
    windowDragCleanupRef.current?.();
    windowDragCleanupRef.current = null;
    miniDragCleanupRef.current?.();
    miniDragCleanupRef.current = null;
  }, []);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: 0 });
  }, []);

  const visibleBidSheetTip = bidSheetBidAttempted && controller.bidError
    ? controller.bidError
    : bidSheetTip;

  useEffect(() => {
    const miniVideo = miniVideoRef.current;
    const sourceVideo = document.querySelector<HTMLVideoElement>('.nativeLiveVideo');
    if (!miniVideo || !sourceVideo) return undefined;

    const syncMiniPlayback = () => {
      const sourceTime = sourceVideo.currentTime;
      if (Number.isFinite(sourceTime) && Math.abs((miniVideo.currentTime || 0) - sourceTime) > 0.45) {
        try {
          miniVideo.currentTime = sourceTime;
        } catch {
          // Some remote sources are not seekable until enough metadata is available.
        }
      }
      if (sourceVideo.paused) {
        miniVideo.pause();
      } else {
        void miniVideo.play().catch(() => undefined);
      }
    };

    syncMiniPlayback();
    miniVideo.addEventListener('loadedmetadata', syncMiniPlayback);
    miniVideo.addEventListener('canplay', syncMiniPlayback);
    const syncTimer = window.setInterval(syncMiniPlayback, 1000);

    return () => {
      window.clearInterval(syncTimer);
      miniVideo.removeEventListener('loadedmetadata', syncMiniPlayback);
      miniVideo.removeEventListener('canplay', syncMiniPlayback);
    };
  }, [liveSource]);

  useGSAP(() => {
    const root = rootRef.current;
    if (!root || window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    gsap.fromTo('.liveProductDetailBackdrop', { opacity: 0 }, { opacity: 1, duration: 0.2, ease: 'power2.out' });
    gsap.fromTo('.liveProductDetailSheet', { yPercent: 100 }, { yPercent: 0, duration: 0.34, ease: 'power3.out' });
    gsap.from('.liveProductDetailMini', { opacity: 0, delay: 0.1, duration: 0.24, ease: 'power2.out' });
    gsap.from('.liveProductDetailBuyBar', { y: 54, opacity: 0, delay: 0.1, duration: 0.28, ease: 'power3.out' });
  }, { scope: rootRef, dependencies: [lot.id], revertOnUpdate: true });

  useGSAP(() => {
    if (!bidSheetOpen && !bidAuthPanelOpen) return;
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    gsap.fromTo('.liveProductDetailBidSheet', { y: 42, opacity: 0 }, { y: 0, opacity: 1, duration: 0.22, ease: 'power2.out' });
  }, { scope: rootRef, dependencies: [bidSheetOpen, bidAuthPanelOpen], revertOnUpdate: true });

  const scrollToTab = (tab: DetailTab) => {
    setActiveTab(tab);
    const scroller = scrollRef.current;
    const target = scroller?.querySelector<HTMLElement>(`[data-detail-tab="${tab}"]`);
    if (!scroller || !target) return;
    scroller.scrollTo({ top: Math.max(0, target.offsetTop - 112), behavior: 'smooth' });
  };

  const syncSearchHeaderProgress = () => {
    const scroller = scrollRef.current;
    const hero = scroller?.querySelector<HTMLElement>('.liveProductDetailHero');
    if (!scroller || !hero) {
      setSearchHeaderProgress(0);
      return;
    }
    const heroHeight = Math.max(hero.offsetHeight, 1);
    const start = heroHeight * 0.52;
    const end = heroHeight * 0.86;
    setSearchHeaderProgress(clamp((scroller.scrollTop - start) / (end - start), 0, 1));
  };

  const syncActiveTab = () => {
    const scroller = scrollRef.current;
    if (!scroller) return;
    syncSearchHeaderProgress();
    const nextTab = DETAIL_TABS.reduce<DetailTab>((active, tab) => {
      const target = scroller.querySelector<HTMLElement>(`[data-detail-tab="${tab.id}"]`);
      if (target && target.offsetTop <= scroller.scrollTop + 136) return tab.id;
      return active;
    }, 'product');
    setActiveTab(nextTab);
  };

  const openBidSheet = () => {
    if (action.disabled) {
      if (action.tip) actions.showNotice(action.tip);
      return;
    }
    setBidSheetTip('');
    setBidSheetBidAttempted(false);
    setBidSheetOpen(true);
  };

  const closeBidSheet = () => {
    setBidSheetOpen(false);
    setBidSheetTip('');
    setBidSheetBidAttempted(false);
    if (bidAuthPanelOpen) actions.closeBuyerAuthPanel();
  };

  const showBidSheetTip = (message: string) => {
    setBidSheetTip(message);
    actions.showNotice(message);
  };

  const submitBidFromSheet = (amount: number) => {
    setBidSheetBidAttempted(true);
    setBidSheetTip('');
    void actions.submitBid(amount);
  };

  const openAuctionFromDetail = () => {
    onClose();
    window.setTimeout(() => actions.openAuctionPanel('current'), 0);
  };

  const measureSheetBounds = () => {
    const rootHeight = rootRef.current?.getBoundingClientRect().height || window.innerHeight || 844;
    const compactHeight = Math.min(rootHeight * 0.72, 680);
    const minHeight = clamp(compactHeight, Math.min(390, rootHeight - 34), Math.max(390, rootHeight - 34));
    const lowerHeight = Math.max(160, Math.min(minHeight - 1, rootHeight * 0.34));
    return {
      lower: lowerHeight,
      min: minHeight,
      max: Math.max(minHeight, rootHeight),
    };
  };

  const currentSheetHeight = () => {
    const { min, max } = measureSheetBounds();
    return sheetRef.current?.getBoundingClientRect().height || (sheetMode === 'expanded' ? max : min);
  };

  const updateSheetDrag = (clientY: number) => {
    const drag = dragStateRef.current;
    if (!drag) return;
    const { lower, max } = measureSheetBounds();
    const nextHeight = clamp(drag.startHeight + drag.startY - clientY, lower, max);
    drag.lastHeight = nextHeight;
    setSheetHeight(nextHeight);
  };

  const finishSheetDragAt = () => {
    const drag = dragStateRef.current;
    if (!drag) return;
    const { min, max } = measureSheetBounds();
    const nextMode = drag.lastHeight > min + (max - min) * 0.42 ? 'expanded' : 'compact';
    dragStateRef.current = null;
    windowDragCleanupRef.current?.();
    windowDragCleanupRef.current = null;
    setSheetHeight(null);
    if (drag.lastHeight < min) {
      onClose();
      return;
    }
    if (nextMode !== 'expanded') setSearchHeaderProgress(0);
    setSheetMode(nextMode);
  };

  const handleSheetDragStart = (event: ReactPointerEvent<HTMLElement>) => {
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    const startHeight = currentSheetHeight();
    dragStateRef.current = {
      pointerId: event.pointerId,
      startY: event.clientY,
      startHeight,
      lastHeight: startHeight,
      mode: sheetMode,
    };
    try {
      event.currentTarget.setPointerCapture?.(event.pointerId);
    } catch {
      // Window listeners below still keep the drag responsive when capture is unavailable.
    }
    windowDragCleanupRef.current?.();
    const handleWindowMove = (moveEvent: PointerEvent) => {
      if (moveEvent.pointerId !== event.pointerId) return;
      updateSheetDrag(moveEvent.clientY);
      moveEvent.preventDefault();
    };
    const handleWindowEnd = (endEvent: PointerEvent) => {
      if (endEvent.pointerId !== event.pointerId) return;
      finishSheetDragAt();
    };
    window.addEventListener('pointermove', handleWindowMove, { passive: false });
    window.addEventListener('pointerup', handleWindowEnd);
    window.addEventListener('pointercancel', handleWindowEnd);
    windowDragCleanupRef.current = () => {
      window.removeEventListener('pointermove', handleWindowMove);
      window.removeEventListener('pointerup', handleWindowEnd);
      window.removeEventListener('pointercancel', handleWindowEnd);
    };
    setSheetHeight(startHeight);
  };

  const handleSheetDragMove = (event: ReactPointerEvent<HTMLElement>) => {
    const drag = dragStateRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    updateSheetDrag(event.clientY);
    event.preventDefault();
  };

  const finishSheetDrag = (event: ReactPointerEvent<HTMLElement>) => {
    const drag = dragStateRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    try {
      event.currentTarget.releasePointerCapture?.(event.pointerId);
    } catch {
      // Capture can already be gone after a browser-level cancel.
    }
    finishSheetDragAt();
  };

  const updateMiniPosition = (clientX: number, clientY: number) => {
    const drag = miniDragStateRef.current;
    if (!drag) return;
    const rawX = drag.startOffsetX + clientX - drag.startClientX;
    const rawY = drag.startOffsetY + clientY - drag.startClientY;
    const deltaX = rawX - drag.startOffsetX;
    const deltaY = rawY - drag.startOffsetY;
    if (Math.hypot(clientX - drag.startClientX, clientY - drag.startClientY) > 4) miniDragMovedRef.current = true;
    const margin = 8;
    const minX = drag.startOffsetX + drag.boundsRect.left + margin - drag.startRect.left;
    const maxX = drag.startOffsetX + drag.boundsRect.right - margin - drag.startRect.right;
    const minY = drag.startOffsetY + drag.boundsRect.top + margin - drag.startRect.top;
    const maxY = drag.startOffsetY + drag.boundsRect.bottom - margin - drag.startRect.bottom;
    setMiniOffset({
      x: clamp(drag.startOffsetX + deltaX, minX, maxX),
      y: clamp(drag.startOffsetY + deltaY, minY, maxY),
    });
  };

  const finishMiniDrag = () => {
    miniDragStateRef.current = null;
    miniDragCleanupRef.current?.();
    miniDragCleanupRef.current = null;
    setMiniDragging(false);
  };

  const handleMiniPointerDown = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    const miniRect = miniRef.current?.getBoundingClientRect();
    const boundsRect = sheetRef.current?.getBoundingClientRect() || rootRef.current?.getBoundingClientRect();
    if (!miniRect || !boundsRect) return;
    event.stopPropagation();
    miniDragMovedRef.current = false;
    miniDragStateRef.current = {
      pointerId: event.pointerId,
      startClientX: event.clientX,
      startClientY: event.clientY,
      startOffsetX: miniOffset.x,
      startOffsetY: miniOffset.y,
      startRect: miniRect,
      boundsRect,
    };
    setMiniDragging(true);
    try {
      event.currentTarget.setPointerCapture?.(event.pointerId);
    } catch {
      // Window listeners below keep dragging reliable if pointer capture is unavailable.
    }
    miniDragCleanupRef.current?.();
    const handleWindowMove = (moveEvent: PointerEvent) => {
      if (moveEvent.pointerId !== event.pointerId) return;
      updateMiniPosition(moveEvent.clientX, moveEvent.clientY);
      moveEvent.preventDefault();
    };
    const handleWindowEnd = (endEvent: PointerEvent) => {
      if (endEvent.pointerId !== event.pointerId) return;
      finishMiniDrag();
    };
    window.addEventListener('pointermove', handleWindowMove, { passive: false });
    window.addEventListener('pointerup', handleWindowEnd);
    window.addEventListener('pointercancel', handleWindowEnd);
    miniDragCleanupRef.current = () => {
      window.removeEventListener('pointermove', handleWindowMove);
      window.removeEventListener('pointerup', handleWindowEnd);
      window.removeEventListener('pointercancel', handleWindowEnd);
    };
  };

  const handleMiniPointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const drag = miniDragStateRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    event.stopPropagation();
    updateMiniPosition(event.clientX, event.clientY);
    event.preventDefault();
  };

  const handleMiniPointerUp = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const drag = miniDragStateRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    event.stopPropagation();
    try {
      event.currentTarget.releasePointerCapture?.(event.pointerId);
    } catch {
      // Capture may already be released by the browser.
    }
    finishMiniDrag();
  };

  const handleMiniClick = (event: ReactMouseEvent<HTMLButtonElement>) => {
    if (miniDragMovedRef.current) {
      miniDragMovedRef.current = false;
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    onClose();
  };

  const handleHeroPointerDown = (event: ReactPointerEvent<HTMLElement>) => {
    heroDragStateRef.current = {
      pointerId: event.pointerId,
      startClientX: event.clientX,
      startClientY: event.clientY,
      width: Math.max(event.currentTarget.getBoundingClientRect().width, 1),
      mode: 'pending',
    };
    setHeroDragOffset(0);
    handleSheetDragStart(event);
  };

  const handleHeroPointerMove = (event: ReactPointerEvent<HTMLElement>) => {
    const drag = heroDragStateRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    const deltaX = event.clientX - drag.startClientX;
    const deltaY = event.clientY - drag.startClientY;
    if (drag.mode === 'pending' && (Math.abs(deltaX) > 8 || Math.abs(deltaY) > 8)) {
      drag.mode = galleryImages.length > 1 && Math.abs(deltaX) > Math.abs(deltaY) * 1.15 ? 'carousel' : 'sheet';
    }
    if (drag.mode === 'carousel') {
      const atStart = activeHeroImageIndex === 0 && deltaX > 0;
      const atEnd = activeHeroImageIndex === galleryImages.length - 1 && deltaX < 0;
      setHeroDragOffset(clamp(atStart || atEnd ? deltaX * 0.34 : deltaX, -drag.width, drag.width));
      event.preventDefault();
      return;
    }
    if (drag.mode === 'sheet') handleSheetDragMove(event);
  };

  const finishHeroPointer = (event: ReactPointerEvent<HTMLElement>) => {
    const drag = heroDragStateRef.current;
    if (drag?.pointerId === event.pointerId && drag.mode === 'carousel') {
      const deltaX = event.clientX - drag.startClientX;
      const threshold = Math.max(42, drag.width * 0.16);
      if (Math.abs(deltaX) > threshold) {
        setHeroImageIndex((index) => clamp(index + (deltaX < 0 ? 1 : -1), 0, Math.max(galleryImages.length - 1, 0)));
      }
      setHeroDragOffset(0);
      event.preventDefault();
    }
    heroDragStateRef.current = null;
    finishSheetDrag(event);
  };

  const cancelHeroPointer = (event: ReactPointerEvent<HTMLElement>) => {
    heroDragStateRef.current = null;
    setHeroDragOffset(0);
    finishSheetDrag(event);
  };

  return (
    <section
      className={`liveProductDetailOverlay ${sheetMode === 'expanded' ? 'isExpanded hasHeroChrome' : 'isCompact'} ${sheetHeight !== null ? 'isDragging' : ''} ${miniDragging ? 'isMiniDragging' : ''} ${effectiveSearchProgress > 0.04 ? 'hasSearchHeader' : ''}`}
      aria-label="直播商品详情"
      ref={rootRef}
      style={rootStyle}
    >
      <button type="button" className="liveProductDetailBackdrop" aria-label="关闭商品详情" onClick={onClose} />

      <article className="liveProductDetailSheet" ref={sheetRef}>
        <div
          className="liveProductDetailDragZone"
          aria-label="拖动商品详情卡片"
          onPointerDown={handleSheetDragStart}
          onPointerMove={handleSheetDragMove}
          onPointerUp={finishSheetDrag}
          onPointerCancel={finishSheetDrag}
        >
          <span />
        </div>

        <button type="button" className="liveProductDetailSheetClose" onClick={onClose} aria-label="关闭商品详情"><X size={24} /></button>

        <div className="liveProductDetailHeroChrome" aria-label="图片顶部操作">
          <button type="button" aria-label="关闭商品详情" onClick={onClose}><X size={26} /></button>
          <span>
            <button type="button" aria-label="搜索商品" onClick={() => actions.showNotice('搜索功能随商品详情同步')}><Search size={24} /></button>
            <button type="button" aria-label="购物车" onClick={openAuctionFromDetail}><ShoppingCart size={23} /></button>
            <button type="button" aria-label="收藏" onClick={() => actions.showNotice('已收藏当前拍品')}><Heart size={23} /></button>
            <button type="button" aria-label="更多" onClick={() => actions.showNotice('更多商品服务暂未开放')}><MoreHorizontal size={24} /></button>
          </span>
        </div>

        <header className="liveProductDetailTop">
          <div className="liveProductDetailTopMain">
            <button type="button" aria-label="关闭商品详情" onClick={onClose}><X size={24} /></button>
            <label>
              <Search size={16} />
              <span>搜索喜欢的商品</span>
            </label>
            <button type="button" aria-label="收藏" onClick={() => actions.showNotice('已收藏当前拍品')}><Heart size={20} /></button>
            <button type="button" aria-label="购物车" onClick={openAuctionFromDetail}><ShoppingCart size={20} /></button>
            <button type="button" aria-label="更多" onClick={() => actions.showNotice('更多商品服务暂未开放')}><MoreHorizontal size={21} /></button>
          </div>
          <nav className="liveProductDetailTabs" aria-label="商品详情分区">
            {DETAIL_TABS.map((tab) => (
              <button type="button" className={activeTab === tab.id ? 'active' : ''} key={tab.id} onClick={() => scrollToTab(tab.id)}>
                {tab.label}
              </button>
            ))}
          </nav>
        </header>

        <button
          type="button"
          className="liveProductDetailMini"
          ref={miniRef}
          onClick={handleMiniClick}
          onPointerDown={handleMiniPointerDown}
          onPointerMove={handleMiniPointerMove}
          onPointerUp={handleMiniPointerUp}
          onPointerCancel={handleMiniPointerUp}
          aria-label="拖动直播小窗，轻点返回直播"
        >
          <video ref={miniVideoRef} src={liveSource} autoPlay muted loop playsInline preload="metadata" poster={activeHeroImage} />
          <span>{wsState === '已连接' ? '直播中' : wsState}</span>
          <b>{anchorName || roomName}</b>
        </button>

        <div className="liveProductDetailScroll" ref={scrollRef} onScroll={syncActiveTab}>
          <section
            className="liveProductDetailHero"
            data-detail-tab="product"
            aria-labelledby={sectionTitleId('product')}
            onPointerDown={handleHeroPointerDown}
            onPointerMove={handleHeroPointerMove}
            onPointerUp={finishHeroPointer}
            onPointerCancel={cancelHeroPointer}
          >
            {galleryImages.length ? (
              <div className={`liveProductDetailGallery ${heroDragOffset ? 'isDragging' : ''}`} style={galleryTrackStyle}>
                {galleryImages.map((imageUrl, index) => (
                  <img key={`${imageUrl}-${index}`} src={imageUrl} alt={index === 0 ? lot.title : `${lot.title} ${index + 1}`} draggable={false} />
                ))}
              </div>
            ) : <span>商品图待同步</span>}
            <i>{displayStateLabel(displayState)}</i>
            {galleryImages.length > 1 ? (
              <>
                <b className="liveProductDetailGalleryCount">{activeHeroImageIndex + 1}/{galleryImages.length}</b>
                <div
                  className="liveProductDetailGalleryDots"
                  aria-label="商品图片分页"
                  onPointerDown={(event) => event.stopPropagation()}
                  onPointerMove={(event) => event.stopPropagation()}
                  onPointerUp={(event) => event.stopPropagation()}
                  onPointerCancel={(event) => event.stopPropagation()}
                >
                  {galleryImages.map((imageUrl, index) => (
                    <button
                      type="button"
                      key={`${imageUrl}-dot`}
                      className={index === activeHeroImageIndex ? 'active' : ''}
                      aria-label={`查看第 ${index + 1} 张商品图`}
                      onClick={() => {
                        setHeroImageIndex(index);
                        setHeroDragOffset(0);
                      }}
                    />
                  ))}
                </div>
              </>
            ) : null}
          </section>

          <section className="liveProductDetailCard liveProductDetailSummary" aria-labelledby={sectionTitleId('product')}>
            <h2 id={sectionTitleId('product')}>商品</h2>
            <p className="liveProductDetailPrice">
              <span>当前价</span>
              <b>¥{majorPriceText(price)}</b>
              <em>{displayStateLabel(displayState)}</em>
            </p>
            <h1>{lot.title}</h1>
            <div className="liveProductDetailBadges">
              <span>保证金 {formatLotDeposit(lot)}</span>
              <span>起拍价 {formatMoney(lot.rule.startPrice)}</span>
              <span>加价幅度 {formatMoney(lot.rule.minIncrement)}</span>
            </div>
            <div className="liveProductDetailService">
              <span>正品保障</span>
              <span>平台担保</span>
              <span>订单支付以本页为准</span>
            </div>
          </section>

          <section className="liveProductDetailCard liveProductDetailAuction">
            <h2>拍卖流程</h2>
            <div>
              <span className="done">确认保证金</span>
              <span className={displayState === 'live' ? 'active' : 'done'}>参与出价</span>
              <span className={displayState === 'pendingPayment' ? 'active' : displayState === 'finished' ? 'done' : ''}>截拍确认</span>
              <span className={displayState === 'finished' ? 'done' : ''}>订单支付</span>
            </div>
          </section>

          <section className="liveProductDetailCard liveProductDetailBids">
            <h2>出价记录</h2>
            {acceptedBids.length ? acceptedBids.map((bid) => (
              <p key={bid.id || `${bid.userId}-${bid.createdAtUnixMs || bid.amount.amount}`}>
                <span>{bid.nickname || `拍友${bid.userId.slice(-4)}`}</span>
                <b>{formatMoney(bid.amount)}</b>
                <em>{formatEventTime(bid.createdAtUnixMs)}</em>
              </p>
            )) : <p className="empty">暂时还没有出价，等第一位拍友举牌</p>}
          </section>

          <section className="liveProductDetailCard liveProductDetailReviews" data-detail-tab="reviews" aria-labelledby={sectionTitleId('reviews')}>
            <h2 id={sectionTitleId('reviews')}>评价</h2>
            <div className="liveProductDetailScore">
              <strong>4.9</strong>
              <span>店铺体验分</span>
              <small>描述相符高 · 发货稳定 · 服务响应快</small>
            </div>
            <p className="empty">该商品暂无评价，成交后可在订单中补充体验。</p>
          </section>

          <section className="liveProductDetailCard liveProductDetailCopy" data-detail-tab="detail" aria-labelledby={sectionTitleId('detail')}>
            <h2 id={sectionTitleId('detail')}>详情</h2>
            <p>{lot.description?.trim() || '主播正在讲解这件拍品，实际细节以直播展示、商品凭证和订单确认为准。'}</p>
            {trustCards.length ? (
              <div className="liveProductTrustGrid">
                {trustCards.map((card) => (
                  <article key={card.id}>
                    <b>{card.title || trustCardLabel(card)}</b>
                    {card.content ? <p>{card.content}</p> : null}
                    {card.imageUrl ? <img src={card.imageUrl} alt={card.title || trustCardLabel(card)} /> : null}
                  </article>
                ))}
              </div>
            ) : <p className="empty">暂无更多凭证，等待主播补充说明。</p>}
          </section>

          <section className="liveProductDetailCard liveProductDetailRecommend" data-detail-tab="recommend" aria-labelledby={sectionTitleId('recommend')}>
            <h2 id={sectionTitleId('recommend')}>推荐</h2>
            {recommendations.length ? (
              <div>
                {recommendations.map((item) => (
                  <button type="button" key={item.id} onClick={() => onSelectLot(item)}>
                    {item.imageUrl ? <img src={item.imageUrl} alt="" loading="lazy" /> : <span>拍</span>}
                    <b>{item.title}</b>
                    <strong>{formatMoney(currentPrice(item))}</strong>
                  </button>
                ))}
              </div>
            ) : <p className="empty">本场暂时没有更多推荐，留意主播继续上新。</p>}
          </section>
        </div>

        <footer className="liveProductDetailBuyBar">
          <button type="button" onClick={() => actions.showNotice('店铺资料随直播间同步')}><Store size={19} /><span>店铺</span></button>
          <button type="button" onClick={() => actions.showNotice('客服已收到咨询')}><MessageCircle size={19} /><span>客服</span></button>
          <button type="button" onClick={openAuctionFromDetail}><ShoppingCart size={19} /><span>橱窗</span></button>
          <button type="button" className="primary" disabled={action.disabled} onClick={openBidSheet}>{action.label}</button>
        </footer>
      </article>

      {bidSheetOpen || bidAuthPanelOpen ? (
        <div className="liveProductDetailBidMask" onClick={closeBidSheet}>
          <section className="liveBidSheet liveProductSheet liveProductDetailBidSheet" role="dialog" aria-modal="true" aria-label={bidAuthPanelOpen ? '登录后出价' : '出价'} onClick={(event) => event.stopPropagation()}>
            <button type="button" className="bidSheetClose" onClick={closeBidSheet} aria-label="关闭出价面板"><X size={19} /></button>
            {bidAuthPanelOpen ? (
              <BuyerAuthPanel auth={buyerAuth} />
            ) : (
              <>
                <CurrentLotCard
                  lot={lot}
                  serverTimeUnixMs={room.serverTimeUnixMs}
                  serverTimeReceivedAtUnixMs={room.serverTimeReceivedAtUnixMs}
                  displayState={displayState}
                />
                {visibleBidSheetTip ? <p className="bidSheetInlineNotice" role="alert">{visibleBidSheetTip}</p> : null}
                <BidPanel
                  lot={lot}
                  loading={controller.isBidPending}
                  disabledReason={accountRoleMessage || ''}
                  onBid={submitBidFromSheet}
                  onTip={showBidSheetTip}
                />
              </>
            )}
          </section>
        </div>
      ) : null}
    </section>
  );
}
