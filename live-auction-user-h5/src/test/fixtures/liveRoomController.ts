/**
 * LiveRoomController 的确定性替身。
 *
 * 用途有两层：
 * 1. 驱动 LiveRoomView 的表现冻结快照（G2），不需要网络或 WebSocket。
 * 2. 作为控制器返回形状的契约样本 —— L3 拆分 useLiveRoomController 时，
 *    只要真实 hook 的返回形状仍与本文件对齐，视图就一定不受影响。
 *
 * 见 docs/Refactor/H5_TARGET_BLUEPRINT.md L3。
 */
import { SNAPSHOT_NOW_MS } from '../domSnapshot';
import { LOT_STATUS, type Lot, type LotStatus, type Money, type OrderSummary } from '../../shared/api/types';
import type { LiveRoomController } from '../../features/live-room/hooks/useLiveRoomController';

const ROOM_ID = 'room-fixture-01';
const ME_ID = 'user-me';

function money(amount: number): Money {
  return { amount, currency: 'CNY' };
}

export function fixtureLot(overrides: Partial<Lot> = {}): Lot {
  return {
    id: 'lot-fixture-01',
    roomId: ROOM_ID,
    title: '和田玉平安扣',
    description: '固定加价 50 元，封顶价 1200 元。',
    imageUrl: 'https://assets.example.test/lot-fixture-01.jpg',
    galleryImageUrls: [],
    status: LOT_STATUS.LIVE,
    currentPrice: money(68_000),
    leadingUserId: 'user-rival',
    leadingNickname: '拍友A',
    stats: { participantCount: 42, bidCount: 17 },
    startedAtUnixMs: SNAPSHOT_NOW_MS - 60_000,
    endsAtUnixMs: SNAPSHOT_NOW_MS + 30_000,
    createdAtUnixMs: SNAPSHOT_NOW_MS - 600_000,
    updatedAtUnixMs: SNAPSHOT_NOW_MS - 1_000,
    rule: {
      startPrice: money(0),
      minIncrement: money(5_000),
      capPrice: money(120_000),
      durationSeconds: 120,
      antiSnipeWindowSeconds: 10,
      antiSnipeExtendSeconds: 15,
      maxExtendCount: 3,
    },
    version: 7,
    trustCards: [],
    ...overrides,
  };
}

export function fixtureOrder(overrides: Partial<OrderSummary> = {}): OrderSummary {
  return {
    id: 'order-fixture-01',
    lotId: 'lot-fixture-01',
    roomId: ROOM_ID,
    lotTitle: '和田玉平安扣',
    lotImageUrl: 'https://assets.example.test/lot-fixture-01.jpg',
    buyerUserId: ME_ID,
    buyerNickname: '我',
    amount: money(72_000),
    status: 'PENDING_PAYMENT',
    paymentStatus: 'INIT',
    createdAtUnixMs: SNAPSHOT_NOW_MS - 5_000,
    updatedAtUnixMs: SNAPSHOT_NOW_MS - 5_000,
    expiresAtUnixMs: SNAPSHOT_NOW_MS + 900_000,
    ...overrides,
  };
}

const noop = () => {};
const asyncNoop = async () => {};

/**
 * 构造一个完整的 controller 替身。
 * 顶层字段用 overrides 覆盖；嵌套的 buyerAuth / auctionPanel / actions 做浅合并。
 */
export function fixtureLiveRoomController(
  overrides: Partial<LiveRoomController> = {},
): LiveRoomController {
  const currentLot = 'currentLot' in overrides ? overrides.currentLot ?? null : fixtureLot();

  const base: LiveRoomController = {
    roomId: ROOM_ID,
    room: {
      roomId: ROOM_ID,
      snapshot: {
        roomId: ROOM_ID,
        roomName: '严选珠宝直播间',
        anchorName: '严选主播',
        liveSourceUrl: '',
        onlineCount: 1_286,
        serverTimeUnixMs: SNAPSHOT_NOW_MS,
        currentLot,
        ranking: [],
        recentBids: [],
      },
      eventState: { lastEvent: null, source: 'snapshot' },
      localOptimistic: {},
      currentLot,
      ranking: [],
      recentBids: [],
      serverTimeUnixMs: SNAPSHOT_NOW_MS,
      serverTimeReceivedAtUnixMs: SNAPSHOT_NOW_MS,
      orders: [],
      paidLotIds: {},
    },
    loading: false,
    error: '',
    roomName: '严选珠宝直播间',
    anchorName: '严选主播',
    currentLot,
    ranking: [
      { rank: 1, userId: 'user-rival', nickname: '拍友A', avatarUrl: '', amount: money(68_000), bidAtUnixMs: SNAPSHOT_NOW_MS - 4_000, isMe: false },
      { rank: 2, userId: ME_ID, nickname: '我', avatarUrl: '', amount: money(63_000), bidAtUnixMs: SNAPSHOT_NOW_MS - 9_000, isMe: true },
      { rank: 3, userId: 'user-third', nickname: '收藏顾问', avatarUrl: '', amount: money(58_000), bidAtUnixMs: SNAPSHOT_NOW_MS - 15_000, isMe: false },
    ],
    meId: ME_ID,
    wsState: '已连接',
    notices: [],
    bidError: '',
    isBidPending: false,
    accountRoleMessage: '',
    showBuyerAuth: false,
    bidAuthPanelOpen: false,
    buyerAuth: {
      mode: 'login',
      username: '',
      password: '',
      nickname: '',
      busy: false,
      error: '',
      setMode: noop,
      setUsername: noop,
      setPassword: noop,
      setNickname: noop,
      submit: asyncNoop,
    },
    auctionPanel: {
      open: false,
      tab: 'current',
      lots: [],
      loading: false,
      error: '',
    },
    resultLot: null,
    visibleResultOrder: null,
    payOrder: null,
    depositPrompt: null,
    actions: {
      submitBid: asyncNoop,
      confirmDepositPayment: asyncNoop,
      closeDepositPrompt: noop,
      closeResult: noop,
      nextLot: noop,
      openAuctionPanel: noop,
      closeBuyerAuthPanel: noop,
      requireBuyerAuth: noop,
      closeAuctionPanel: noop,
      setAuctionPanelTab: noop,
      showNotice: noop,
      refreshRoomLots: async () => [],
      refreshOrders: async () => ({ orders: [], total: 0, page: 1, pageSize: 20 }),
      setPayOrder: noop,
      markPaymentStarted: noop,
      handlePaymentPaid: asyncNoop,
    },
  };

  return {
    ...base,
    ...overrides,
    buyerAuth: { ...base.buyerAuth, ...overrides.buyerAuth },
    auctionPanel: { ...base.auctionPanel, ...overrides.auctionPanel },
    actions: { ...base.actions, ...overrides.actions },
  };
}

/** 直播间需要冻结的三个终态。 */
export const LIVE_ROOM_SNAPSHOT_CASES: ReadonlyArray<{ name: string; status: LotStatus }> = [
  { name: 'live', status: LOT_STATUS.LIVE },
  { name: 'settled', status: LOT_STATUS.SETTLED },
  { name: 'cancelled', status: LOT_STATUS.CANCELLED },
];
