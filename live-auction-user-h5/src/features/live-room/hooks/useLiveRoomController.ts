import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { isBiddableLotStatus, isPrivateRefreshEventType, isSettlementEventType, lotIdFromPublicEvent } from '../../../entities/auction/model/status';
import { ownOrderForLot } from '../../../entities/order/model/privacy';
import { createDepositHold, listPublicRooms, listRoomLots, placeBid } from '../../auction/api/auctionApi';
import { createIdempotencyKey } from '../../../shared/lib/idempotency';
import { getServerNowMs } from '../../../shared/lib/time';
import { normalizeMoney } from '../../../shared/api/adapters';
import { AppApiError, businessErrorMessageFromUnknown, isAuthRequiredError } from '../../../shared/api/errors';
import { AUCTION_EVENT_TYPE, RESULT_CODE, type AuctionSocketEvent, type Lot, type OrderSummary, type PaymentSummary } from '../../../shared/api/types';
import { normalizeBuyerUsername, validateBuyerCredentials } from '../../../shared/auth/credentialRules';
import { useAuthSession } from '../../../shared/auth/useAuthSession';
import { DEFAULT_ROOM_VISUAL_PROFILE } from '../../../shared/config/demoRooms';
import type { DeliveryAddress } from '../../../shared/address/addressBook';
import { noticeForAuctionEvent } from '../model/notices';
import { lotEndsAtPassed } from '../model/lotDisplayState';
import { useAuctionRoom } from './useAuctionRoom';
import { useAuctionSocket } from './useAuctionSocket';

const DEFAULT_ACTIVITY_QUERY = { page: 1, pageSize: 20 };
const NOTICE_VISIBLE_MS = 3400;

export type AuctionPanelTab = 'current' | 'queue' | 'mine';
type DepositPrompt = {
  lot: Lot;
  bidAmount: number;
  userId: string;
};

function bidFailureMessage(reason: unknown, lot?: Lot): string {
  const businessMessage = businessErrorMessageFromUnknown(reason, { lot });
  if (businessMessage) return businessMessage;
  const rawMessage = reason instanceof Error ? reason.message : typeof reason === 'string' ? reason : '请稍后重试';
  const message = rawMessage
    .replace(/^invalid argument:\s*/i, '')
    .replace(/^操作失败：\s*/i, '');

  if (message.includes('leading bidder must wait') || message.includes('最高价')) return '你当前已经是最高价，等其他人出价后再加价';
  if (message.includes('主播取消') || message.includes('already cancelled')) return message.includes('本件拍品') ? message : '本件拍品已由主播取消，无法继续出价';
  if (message.includes('bid amount is lower')) return '出价金额太低，请按当前加价幅度重新出价';
  if (message.includes('lot is not live') || message.includes('auction has ended')) return '当前商品还未开始或已结束';
  if (message.includes('currency')) return '出价币种异常，请刷新后重试';
  if (message.includes('runtime state is missing')) return '竞拍状态正在同步，请稍后重试';

  return message.startsWith('操作失败') ? message : `操作失败：${message}`;
}

function shouldOpenBuyerAuth(reason: unknown): boolean {
  return isAuthRequiredError(reason);
}

function nicknameFromUsername(username: string): string {
  const normalized = username.trim();
  return normalized ? `买家${normalized.slice(0, 8)}` : `买家${Date.now().toString().slice(-4)}`;
}

function eventMayChangeLotList(event: AuctionSocketEvent): boolean {
  return event.type === AUCTION_EVENT_TYPE.LOT_CREATED ||
    event.type === AUCTION_EVENT_TYPE.LOT_QUEUED ||
    event.type === AUCTION_EVENT_TYPE.LOT_STARTED ||
    event.type === AUCTION_EVENT_TYPE.LOT_UPDATED ||
    event.type === AUCTION_EVENT_TYPE.AUCTION_EXTENDED ||
    event.type === AUCTION_EVENT_TYPE.AUCTION_CLOSED ||
    event.type === AUCTION_EVENT_TYPE.LOT_SETTLED ||
    event.type === AUCTION_EVENT_TYPE.LOT_CANCELLED ||
    event.type === AUCTION_EVENT_TYPE.ORDER_CREATED ||
    event.type === AUCTION_EVENT_TYPE.PAYMENT_SUCCESS;
}

function isCancellationEvent(event: AuctionSocketEvent): boolean {
  return event.type === AUCTION_EVENT_TYPE.LOT_CANCELLED ||
    (event.type === AUCTION_EVENT_TYPE.AUCTION_CLOSED && event.lot?.status === 'LOT_STATUS_CANCELLED');
}

export function useLiveRoomController(roomId: string) {
  const { user, status, reason, authMode, ensureReadyForBid, loginBuyer, registerBuyer, resetBuyerPassword } = useAuthSession();
  const {
    room,
    loading,
    error,
    reload,
    refreshOrders,
    refreshLotResult,
    applyEvent,
    markBidStarted,
    markBidSettled,
    markPaymentStarted,
    markPaymentSettled,
  } = useAuctionRoom(roomId);

  const [notices, setNotices] = useState<string[]>([]);
  const [bidError, setBidError] = useState('');
  const [bidding, setBidding] = useState(false);
  const [authFormMode, setAuthFormMode] = useState<'login' | 'register' | 'reset'>('login');
  const [authUsername, setAuthUsername] = useState('');
  const [authPassword, setAuthPassword] = useState('');
  const [authNickname, setAuthNickname] = useState('');
  const [authBusy, setAuthBusy] = useState(false);
  const [authError, setAuthError] = useState('');
  const [authPanelForcedOpen, setAuthPanelForcedOpen] = useState(false);
  const [resultLot, setResultLot] = useState<Lot | null>(null);
  const [resultOrder, setResultOrder] = useState<OrderSummary | null>(null);
  const [payOrder, setPayOrder] = useState<OrderSummary | null>(null);
  const [depositPrompt, setDepositPrompt] = useState<DepositPrompt | null>(null);
  const [auctionPanelOpen, setAuctionPanelOpen] = useState(false);
  const [auctionPanelTab, setAuctionPanelTab] = useState<AuctionPanelTab>('current');
  const [roomLots, setRoomLots] = useState<Lot[]>([]);
  const [roomLotsLoading, setRoomLotsLoading] = useState(false);
  const [roomLotsError, setRoomLotsError] = useState('');
  const [publicRoomName, setPublicRoomName] = useState('');
  const noticeTimerRef = useRef<number | null>(null);
  const visibleNoticeRef = useRef('');
  const dismissedResultLotIdsRef = useRef<Set<string>>(new Set());
  const shownResultLotIdsRef = useRef<Set<string>>(new Set());
  const settlementSyncKeyRef = useRef('');

  const meId = user?.id ?? '';
  const currentLot = room.currentLot;
  const roomName = room.snapshot?.roomName || room.snapshot?.anchorName || publicRoomName || DEFAULT_ROOM_VISUAL_PROFILE.roomName;
  const anchorName = room.snapshot?.anchorName || room.snapshot?.roomName || publicRoomName || DEFAULT_ROOM_VISUAL_PROFILE.anchorName;
  const isBidPending = Boolean(room.localOptimistic.pendingBid) || bidding;
  const accountRoleMessage = status !== 'authenticated' && reason === '该账号不是买家账号' ? reason : '';
  const showBuyerAuth = !user && (authMode === 'real' || reason?.includes('请先登录'));
  const bidAuthPanelOpen = !user && authPanelForcedOpen;

  const ranking = useMemo(
    () => room.ranking.map((item) => ({ ...item, isMe: item.userId === meId })),
    [meId, room.ranking],
  );

  const visibleResultOrder = ownOrderForLot(resultOrder || room.activeOrder, meId, resultLot?.id);

  useEffect(() => {
    let disposed = false;
    void listPublicRooms()
      .then((rooms) => {
        if (disposed) return;
        const matchedRoom = rooms.find((item) => item.id === roomId);
        setPublicRoomName(matchedRoom?.name || '');
      })
      .catch(() => {
        if (!disposed) setPublicRoomName('');
      });
    return () => {
      disposed = true;
    };
  }, [roomId]);

  const refreshRoomLots = useCallback(async () => {
    setRoomLotsLoading(true);
    setRoomLotsError('');
    try {
      const lots = await listRoomLots(roomId);
      setRoomLots(lots);
      return lots;
    } catch (e) {
      const message = e instanceof Error ? e.message : '商品队列加载失败';
      setRoomLotsError(message);
      throw e;
    } finally {
      setRoomLotsLoading(false);
    }
  }, [roomId]);

  const pushNotice = useCallback((notice: string) => {
    const nextNotice = notice.trim();
    if (!nextNotice || visibleNoticeRef.current === nextNotice) return;

    if (noticeTimerRef.current) {
      window.clearTimeout(noticeTimerRef.current);
    }

    visibleNoticeRef.current = nextNotice;
    setNotices([nextNotice]);
    noticeTimerRef.current = window.setTimeout(() => {
      visibleNoticeRef.current = '';
      noticeTimerRef.current = null;
      setNotices((current) => (current[0] === nextNotice ? [] : current));
    }, NOTICE_VISIBLE_MS);
  }, []);

  useEffect(() => () => {
    if (noticeTimerRef.current) {
      window.clearTimeout(noticeTimerRef.current);
    }
  }, []);

  const closeTransientPanelsForResult = useCallback(() => {
    setAuctionPanelOpen(false);
    setAuctionPanelTab('current');
    setAuthPanelForcedOpen(false);
    setDepositPrompt(null);
    setPayOrder(null);
    setBidError('');
  }, []);

  const syncPrivateResult = useCallback(async (
    lotId: string,
    options: { showModal?: boolean; refreshOrderList?: boolean; silent?: boolean } = {},
  ) => {
    if (!lotId) return null;
    try {
      const result = await refreshLotResult(lotId);
      const resultLotId = result.lot.id;
      const resultAlreadyVisible = resultLot?.id === resultLotId;
      if (resultAlreadyVisible) {
        setResultLot(result.lot);
        setResultOrder((current) => result.order || (current?.lotId === resultLotId ? current : null));
      }
      const shouldShowModal = (options.showModal ?? true) &&
        !resultAlreadyVisible &&
        !dismissedResultLotIdsRef.current.has(resultLotId) &&
        !shownResultLotIdsRef.current.has(resultLotId);
      if (shouldShowModal) {
        shownResultLotIdsRef.current.add(resultLotId);
        closeTransientPanelsForResult();
        setResultLot(result.lot);
        setResultOrder(result.order || null);
      }
      if (result.order && !options.silent) pushNotice('你的订单已同步');
      if (options.refreshOrderList) await refreshOrders(DEFAULT_ACTIVITY_QUERY).catch(() => undefined);
      return result;
    } catch (e) {
      if (!options.silent) pushNotice(e instanceof Error ? e.message : '成交结果同步失败');
      return null;
    }
  }, [closeTransientPanelsForResult, pushNotice, refreshLotResult, refreshOrders, resultLot]);

  const recoverRealtimeState = useCallback(async () => {
    const snapshot = await reload().catch(() => undefined);
    await refreshRoomLots().catch(() => undefined);
    const lotId = resultLot?.id || currentLot?.id || '';
    if (lotId) {
      await syncPrivateResult(lotId, {
        showModal: Boolean(resultLot),
        refreshOrderList: true,
        silent: true,
      });
    } else {
      await refreshOrders(DEFAULT_ACTIVITY_QUERY).catch(() => undefined);
    }
    return snapshot;
  }, [currentLot?.id, refreshOrders, refreshRoomLots, reload, resultLot, syncPrivateResult]);

  const handleSocketEvent = useCallback((event: AuctionSocketEvent) => {
    if (event.roomId && event.roomId !== roomId) return;
    if (event.type === AUCTION_EVENT_TYPE.SERVER_HEARTBEAT) return;

    const previousLeaderId = room.currentLot?.leadingUserId;
    applyEvent(event);

    const notice = noticeForAuctionEvent(event, meId, previousLeaderId);
    if (notice) pushNotice(notice);
    const cancellationEvent = isCancellationEvent(event);
    if (cancellationEvent) {
      const message = businessErrorMessageFromUnknown(event.reason, { lot: event.lot || currentLot || undefined }) || (event.reason ? `本件拍品已由主播取消，原因：${event.reason}` : '本件拍品已由主播取消');
      setBidding(false);
      setAuthPanelForcedOpen(false);
      setDepositPrompt(null);
      setBidError(message);
      pushNotice(message);
    }
    if (eventMayChangeLotList(event)) void refreshRoomLots().catch(() => undefined);

    if (!cancellationEvent && isSettlementEventType(event.type)) {
      const lotId = lotIdFromPublicEvent(event, resultLot?.id || currentLot?.id || '');
      void syncPrivateResult(lotId, {
        showModal: event.type !== AUCTION_EVENT_TYPE.PAYMENT_SUCCESS,
        refreshOrderList: isPrivateRefreshEventType(event.type),
      });
    }
  }, [applyEvent, currentLot, meId, pushNotice, refreshRoomLots, resultLot?.id, room.currentLot?.leadingUserId, roomId, syncPrivateResult]);

  const wsState = useAuctionSocket(roomId, handleSocketEvent, recoverRealtimeState, meId);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void refreshRoomLots().catch(() => undefined);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [refreshRoomLots]);

  useEffect(() => {
    if (!currentLot || !isBiddableLotStatus(currentLot.status)) return undefined;
    const endsAt = Number(currentLot.endsAtUnixMs || 0);
    if (!Number.isFinite(endsAt) || endsAt <= 0) return undefined;

    const syncKey = `${currentLot.id}:${endsAt}:${currentLot.version}`;
    if (settlementSyncKeyRef.current === syncKey) return undefined;

    const nowMs = getServerNowMs(room.serverTimeUnixMs, room.serverTimeReceivedAtUnixMs);
    const delayMs = Math.max(0, endsAt - nowMs + 120);
    const timer = window.setTimeout(() => {
      settlementSyncKeyRef.current = syncKey;
      void recoverRealtimeState().catch(() => undefined);
    }, delayMs);

    return () => window.clearTimeout(timer);
  }, [
    currentLot,
    recoverRealtimeState,
    room.serverTimeReceivedAtUnixMs,
    room.serverTimeUnixMs,
  ]);

  const submitBuyerAuth = useCallback(async () => {
    if (authBusy) return;
    const validationError = validateBuyerCredentials({
      username: authUsername,
      password: authPassword,
      nickname: authNickname,
      requireNickname: authFormMode === 'register',
    });
    if (validationError) {
      setAuthError(validationError);
      return;
    }

    setAuthBusy(true);
    setAuthError('');
    try {
      const username = normalizeBuyerUsername(authUsername);
      if (authFormMode === 'reset') {
        await resetBuyerPassword(username, authPassword);
        setAuthFormMode('login');
        setAuthPassword('');
        pushNotice('密码已重置，请用新密码登录');
        return;
      }
      if (authFormMode === 'login') await loginBuyer(username, authPassword);
      else await registerBuyer(username, authPassword, authNickname.trim() || nicknameFromUsername(username));
      setBidError('');
      setAuthPanelForcedOpen(false);
        pushNotice(authFormMode === 'login' ? '登录成功，可以继续' : '注册成功，可以继续');
    } catch (e) {
      setAuthError(e instanceof Error ? e.message : '账号处理失败，请重试');
    } finally {
      setAuthBusy(false);
    }
  }, [authBusy, authFormMode, authNickname, authPassword, authUsername, loginBuyer, pushNotice, registerBuyer, resetBuyerPassword]);

  const openAuctionPanel = useCallback((tab: AuctionPanelTab = 'current') => {
    setAuctionPanelTab(tab);
    setAuctionPanelOpen(true);
    setBidError('');
    void reload().catch(() => undefined);
    void refreshRoomLots().catch(() => undefined);
    if (user || tab === 'mine') void refreshOrders(DEFAULT_ACTIVITY_QUERY).catch(() => undefined);
  }, [refreshOrders, refreshRoomLots, reload, user]);

  const openBuyerAuthPanel = useCallback(() => {
    setAuthFormMode('login');
    setAuthPanelForcedOpen(true);
    setAuctionPanelTab('current');
    setAuctionPanelOpen(true);
  }, []);

  const requireBuyerAuth = useCallback(() => {
    setBidError('');
    setDepositPrompt(null);
    setPayOrder(null);
    openBuyerAuthPanel();
  }, [openBuyerAuthPanel]);

  const submitBid = useCallback(async (amount: number) => {
    if (isBidPending) return;
    if (!currentLot) {
      setBidError('当前暂无商品，等待主播讲解');
      return;
    }
    const currentServerNowMs = getServerNowMs(room.serverTimeUnixMs, room.serverTimeReceivedAtUnixMs);
    if (!isBiddableLotStatus(currentLot.status) || lotEndsAtPassed(currentLot, currentServerNowMs)) {
      const message = lotEndsAtPassed(currentLot, currentServerNowMs) ? '当前商品已截拍，正在结算' : '当前商品还未开始或已结束';
      setBidError(message);
      pushNotice(message);
      void recoverRealtimeState().catch(() => undefined);
      return;
    }
    if (meId && currentLot.leadingUserId === meId) {
      const message = '你当前已经是最高价，等其他人出价后再加价';
      setBidError(message);
      pushNotice(message);
      return;
    }

    let session: Awaited<ReturnType<typeof ensureReadyForBid>>;

    try {
      session = await ensureReadyForBid();
    } catch (e) {
      if (shouldOpenBuyerAuth(e)) {
        requireBuyerAuth();
        return;
      }
      const message = bidFailureMessage(e, currentLot);
      setBidError(message);
      pushNotice(message);
      return;
    }

    setBidding(true);
    setBidError('');
    let idempotencyKey = '';

    try {
      idempotencyKey = createIdempotencyKey('bid', currentLot.id, session.user.id, amount);
      markBidStarted(currentLot.id, normalizeMoney(amount), idempotencyKey);

      const res = await placeBid(currentLot.id, {
        amount: normalizeMoney(amount),
        clientKnownVersion: currentLot.version,
        idempotencyKey,
      });

      if (res.accepted) {
        const acceptedEvent = {
          type: AUCTION_EVENT_TYPE.BID_ACCEPTED,
          roomId,
          lotId: currentLot.id,
          lot: res.lot,
          bid: res.bid,
          ranking: res.ranking,
          serverTimeUnixMs: Date.now(),
        };
        applyEvent(acceptedEvent);
        pushNotice(noticeForAuctionEvent(acceptedEvent, session.user.id, currentLot.leadingUserId));
      } else {
        const message = bidFailureMessage(res.rejectReason || '后端未接受本次操作', res.lot || currentLot);
        setBidError(message);
        pushNotice(message);
      }
    } catch (e) {
      if (e instanceof AppApiError && e.code === RESULT_CODE.DEPOSIT_REQUIRED) {
        setAuctionPanelOpen(false);
        setAuthPanelForcedOpen(false);
        setDepositPrompt({ lot: currentLot, bidAmount: amount, userId: session.user.id });
        setBidError('');
        pushNotice('出价前需先支付保证金');
        return;
      }
      if (shouldOpenBuyerAuth(e)) {
        requireBuyerAuth();
        return;
      }
      const message = bidFailureMessage(e, currentLot);
      setBidError(message);
      pushNotice(message);
    } finally {
      markBidSettled(idempotencyKey);
      setBidding(false);
    }
  }, [
    applyEvent,
    currentLot,
    ensureReadyForBid,
    isBidPending,
    markBidSettled,
    markBidStarted,
    meId,
    pushNotice,
    recoverRealtimeState,
    requireBuyerAuth,
    room.serverTimeReceivedAtUnixMs,
    room.serverTimeUnixMs,
    roomId,
  ]);

  const confirmDepositPayment = useCallback(async (address: DeliveryAddress) => {
    if (!depositPrompt) return;
    try {
      await createDepositHold(depositPrompt.lot.id, {
        addressId: address.id,
        idempotencyKey: createIdempotencyKey('deposit', depositPrompt.lot.id, depositPrompt.userId, address.id),
      });
    } catch (e) {
      if (shouldOpenBuyerAuth(e)) {
        requireBuyerAuth();
        return;
      }
      throw e;
    }
    const nextBidAmount = depositPrompt.bidAmount;
    setDepositPrompt(null);
    void submitBid(nextBidAmount);
  }, [depositPrompt, requireBuyerAuth, submitBid]);

  const handlePaymentPaid = useCallback(async (order?: OrderSummary, payment?: PaymentSummary) => {
    markPaymentSettled(order, payment);
    if (order) {
      setResultOrder(order);
      pushNotice(order.paymentStatus === 'SUCCESS' || payment?.status === 'SUCCESS' ? '支付成功，订单已刷新' : '支付状态已同步');
    }

    await refreshOrders(DEFAULT_ACTIVITY_QUERY).catch(() => undefined);
    const lotId = order?.lotId || payOrder?.lotId || resultLot?.id || currentLot?.id || '';
    if (lotId) {
      const latest = await syncPrivateResult(lotId, { showModal: Boolean(resultLot), silent: true });
      if (latest?.order) setResultOrder(latest.order);
    }
    await refreshRoomLots().catch(() => undefined);
  }, [currentLot?.id, markPaymentSettled, payOrder?.lotId, pushNotice, refreshOrders, refreshRoomLots, resultLot, syncPrivateResult]);

  const closeResult = useCallback(() => {
    if (resultLot?.id) dismissedResultLotIdsRef.current.add(resultLot.id);
    setResultLot(null);
    setResultOrder(null);
    setPayOrder(null);
  }, [resultLot]);

  const nextLot = useCallback(() => {
    if (resultLot?.id) dismissedResultLotIdsRef.current.add(resultLot.id);
    setResultLot(null);
    setResultOrder(null);
    void reload().catch(() => undefined);
    void refreshRoomLots().catch(() => undefined);
  }, [refreshRoomLots, reload, resultLot]);

  return {
    roomId,
    room,
    loading,
    error,
    roomName,
    anchorName,
    currentLot,
    ranking,
    meId,
    wsState,
    notices,
    bidError,
    isBidPending,
    accountRoleMessage,
    showBuyerAuth,
    bidAuthPanelOpen,
    buyerAuth: {
      mode: authFormMode,
      username: authUsername,
      password: authPassword,
      nickname: authNickname,
      busy: authBusy,
      error: authError,
      setMode: setAuthFormMode,
      setUsername: setAuthUsername,
      setPassword: setAuthPassword,
      setNickname: setAuthNickname,
      submit: submitBuyerAuth,
    },
    auctionPanel: {
      open: auctionPanelOpen,
      tab: auctionPanelTab,
      lots: roomLots,
      loading: roomLotsLoading,
      error: roomLotsError,
    },
    resultLot,
    visibleResultOrder,
    payOrder,
    depositPrompt,
    actions: {
      submitBid,
      confirmDepositPayment,
      closeDepositPrompt: () => setDepositPrompt(null),
      closeResult,
      nextLot,
      openAuctionPanel,
      closeBuyerAuthPanel: () => setAuthPanelForcedOpen(false),
      requireBuyerAuth,
      closeAuctionPanel: () => setAuctionPanelOpen(false),
      setAuctionPanelTab,
      showNotice: pushNotice,
      refreshRoomLots,
      refreshOrders: () => refreshOrders(DEFAULT_ACTIVITY_QUERY),
      setPayOrder,
      markPaymentStarted,
      handlePaymentPaid,
    },
  };
}

export type LiveRoomController = ReturnType<typeof useLiveRoomController>;
