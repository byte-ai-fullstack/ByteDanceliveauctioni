import {
  AUCTION_EVENT_TYPE,
  type AuctionRoomState,
  type AuctionSocketEvent,
  type BidEvent,
  type Lot,
  type LotResult,
  type Money,
  type OrderSummary,
  type PaymentSummary,
  type RankingItem,
  type RoomSnapshot,
} from '../../../shared/api/types';

export type AuctionRoomAction =
  | { type: 'snapshotReceived'; snapshot: RoomSnapshot }
  | { type: 'eventReceived'; event: AuctionSocketEvent }
  | { type: 'lotResultLoaded'; result: LotResult }
  | { type: 'localBidStarted'; lotId: string; amount: Money; idempotencyKey: string }
  | { type: 'localBidSettled'; idempotencyKey?: string }
  | { type: 'localPaymentStarted'; orderId: string; idempotencyKey: string }
  | { type: 'localPaymentSettled'; order?: OrderSummary; payment?: PaymentSummary }
  | { type: 'ordersLoaded'; orders: OrderSummary[] };

export function createInitialAuctionRoomState(roomId: string): AuctionRoomState {
  return {
    roomId,
    snapshot: null,
    eventState: {
      lastEvent: null,
      source: 'init',
    },
    localOptimistic: {},
    currentLot: null,
    ranking: [],
    recentBids: [],
    serverTimeUnixMs: 0,
    orders: [],
    paidLotIds: {},
  };
}

function mergeRecentBid(recentBids: BidEvent[], bid?: BidEvent): BidEvent[] {
  if (!bid) return recentBids;
  const withoutDuplicate = recentBids.filter((item) => item.id !== bid.id || !bid.id);
  return [bid, ...withoutDuplicate].slice(0, 10);
}

function mergeOrder(orders: OrderSummary[], order?: OrderSummary): OrderSummary[] {
  if (!order) return orders;
  return [order, ...orders.filter((item) => item.id !== order.id)];
}

function orderIsPaid(order?: OrderSummary): boolean {
  return order?.status === 'PAID' || order?.paymentStatus === 'SUCCESS';
}

function mergePaidLotIds(current: Record<string, boolean>, orders: OrderSummary[]): Record<string, boolean> {
  const next = { ...current };
  for (const order of orders) {
    if (order.lotId && orderIsPaid(order)) next[order.lotId] = true;
  }
  return next;
}

function eventServerTime(event: AuctionSocketEvent): number | string | undefined {
  return event.serverTimeUnixMs || event.occurredAtUnixMs;
}

function mergeEventLot(currentLot: Lot | null, eventLot?: Lot): Lot | null {
  if (!eventLot) return currentLot;
  if (!currentLot || currentLot.id !== eventLot.id) return eventLot;

  return {
    ...eventLot,
    imageUrl: eventLot.imageUrl || currentLot.imageUrl,
    galleryImageUrls: eventLot.galleryImageUrls?.length ? eventLot.galleryImageUrls : currentLot.galleryImageUrls,
    description: eventLot.description || currentLot.description,
    trustCards: eventLot.trustCards?.length ? eventLot.trustCards : currentLot.trustCards,
  };
}

function withLot(state: AuctionRoomState, lot?: Lot): Pick<AuctionRoomState, 'currentLot'> {
  return { currentLot: mergeEventLot(state.currentLot, lot) };
}

function withRanking(state: AuctionRoomState, ranking?: RankingItem[]): Pick<AuctionRoomState, 'ranking'> {
  return { ranking: ranking || state.ranking };
}

function snapshotToState(
  state: AuctionRoomState,
  snapshot: RoomSnapshot,
  source: AuctionRoomState['eventState']['source'],
): AuctionRoomState {
  return {
    ...state,
    roomId: snapshot.roomId || state.roomId,
    snapshot,
    eventState: {
      ...state.eventState,
      source,
    },
    currentLot: snapshot.currentLot || null,
    ranking: snapshot.ranking || [],
    recentBids: snapshot.recentBids || [],
    serverTimeUnixMs: snapshot.serverTimeUnixMs || state.serverTimeUnixMs,
    serverTimeReceivedAtUnixMs: snapshot.serverTimeUnixMs ? Date.now() : state.serverTimeReceivedAtUnixMs,
  };
}

function applyPublicEvent(state: AuctionRoomState, event: AuctionSocketEvent): AuctionRoomState {
  const serverTime = eventServerTime(event);
  const base: AuctionRoomState = {
    ...state,
    eventState: {
      lastEvent: event,
      source: 'websocket',
    },
    lastEventId: event.id || state.lastEventId,
    eventSequence: Number(state.eventSequence || 0) + 1,
    serverTimeUnixMs: serverTime || state.serverTimeUnixMs,
    serverTimeReceivedAtUnixMs: serverTime ? Date.now() : state.serverTimeReceivedAtUnixMs,
  };

  if (event.type === AUCTION_EVENT_TYPE.ROOM_SNAPSHOT && event.snapshot) {
    return snapshotToState(base, event.snapshot, 'websocket');
  }

  if (event.type === AUCTION_EVENT_TYPE.BID_ACCEPTED || event.type === AUCTION_EVENT_TYPE.BID_OUTBID) {
    return {
      ...base,
      ...withLot(base, event.lot),
      ...withRanking(base, event.ranking),
      recentBids: mergeRecentBid(base.recentBids, event.bid),
      localOptimistic: event.bid?.id ? { ...base.localOptimistic, pendingBid: undefined } : base.localOptimistic,
    };
  }

  if (
    event.type === AUCTION_EVENT_TYPE.LOT_STARTED ||
    event.type === AUCTION_EVENT_TYPE.LOT_UPDATED ||
    event.type === AUCTION_EVENT_TYPE.AUCTION_EXTENDED ||
    event.type === AUCTION_EVENT_TYPE.AUCTION_CLOSED ||
    event.type === AUCTION_EVENT_TYPE.LOT_SETTLED ||
    event.type === AUCTION_EVENT_TYPE.LOT_CANCELLED ||
    event.type === AUCTION_EVENT_TYPE.ORDER_CREATED
  ) {
    const cancelled = event.type === AUCTION_EVENT_TYPE.LOT_CANCELLED ||
      (event.type === AUCTION_EVENT_TYPE.AUCTION_CLOSED && event.lot?.status === 'LOT_STATUS_CANCELLED');
    return {
      ...base,
      ...withLot(base, event.lot),
      ...withRanking(base, event.ranking),
      localOptimistic: cancelled ? { ...base.localOptimistic, pendingBid: undefined } : base.localOptimistic,
    };
  }

  if (event.type === AUCTION_EVENT_TYPE.PAYMENT_SUCCESS) {
    const lotId = event.lot?.id || event.lotId || '';
    return {
      ...base,
      ...withLot(base, event.lot),
      paidLotIds: lotId ? { ...base.paidLotIds, [lotId]: true } : base.paidLotIds,
    };
  }

  if (event.type === AUCTION_EVENT_TYPE.RANKING_UPDATED) {
    return {
      ...base,
      ...withLot(base, event.lot),
      ...withRanking(base, event.ranking),
    };
  }

  if (event.type === AUCTION_EVENT_TYPE.BID_REJECTED) {
    return {
      ...base,
      recentBids: mergeRecentBid(
        base.recentBids,
        event.bid ? { ...event.bid, accepted: false, rejectReason: event.rejectReason } : undefined,
      ),
      localOptimistic: {
        ...base.localOptimistic,
        pendingBid: undefined,
      },
    };
  }

  return base;
}

export function auctionRoomReducer(state: AuctionRoomState, action: AuctionRoomAction): AuctionRoomState {
  if (action.type === 'snapshotReceived') return snapshotToState(state, action.snapshot, 'snapshot');
  if (action.type === 'eventReceived') return applyPublicEvent(state, action.event);
  if (action.type === 'lotResultLoaded') {
    const orders = mergeOrder(state.orders, action.result.order);
    return {
      ...state,
      currentLot: action.result.lot || state.currentLot,
      activeOrder: action.result.order || state.activeOrder,
      orders,
      paidLotIds: mergePaidLotIds(state.paidLotIds, orders),
    };
  }
  if (action.type === 'localBidStarted') {
    return {
      ...state,
      eventState: { ...state.eventState, source: 'local' },
      localOptimistic: {
        ...state.localOptimistic,
        pendingBid: {
          lotId: action.lotId,
          amount: action.amount,
          idempotencyKey: action.idempotencyKey,
          createdAtUnixMs: Date.now(),
        },
      },
    };
  }
  if (action.type === 'localBidSettled') {
    return {
      ...state,
      localOptimistic: {
        ...state.localOptimistic,
        pendingBid:
          state.localOptimistic.pendingBid?.idempotencyKey === action.idempotencyKey || !action.idempotencyKey
            ? undefined
            : state.localOptimistic.pendingBid,
      },
    };
  }
  if (action.type === 'localPaymentStarted') {
    return {
      ...state,
      eventState: { ...state.eventState, source: 'local' },
      localOptimistic: {
        ...state.localOptimistic,
        pendingPayment: {
          orderId: action.orderId,
          idempotencyKey: action.idempotencyKey,
          createdAtUnixMs: Date.now(),
        },
      },
    };
  }
  if (action.type === 'localPaymentSettled') {
    const orders = mergeOrder(state.orders, action.order);
    return {
      ...state,
      activeOrder: action.order || state.activeOrder,
      payment: action.payment || state.payment,
      orders,
      paidLotIds: mergePaidLotIds(state.paidLotIds, orders),
      localOptimistic: {
        ...state.localOptimistic,
        pendingPayment: undefined,
      },
    };
  }
  if (action.type === 'ordersLoaded') {
    const currentLotOrder = action.orders.find((order) => order.lotId === state.currentLot?.id);
    const existingOrder = state.activeOrder
      ? action.orders.find((order) => order.id === state.activeOrder?.id) || state.activeOrder
      : undefined;

    return {
      ...state,
      orders: action.orders,
      activeOrder: currentLotOrder || existingOrder || action.orders[0],
      paidLotIds: mergePaidLotIds(state.paidLotIds, action.orders),
    };
  }
  return state;
}
