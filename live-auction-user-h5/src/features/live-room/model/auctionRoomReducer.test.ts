import { describe, expect, it } from 'vitest';
import {
  AUCTION_EVENT_TYPE,
  LOT_STATUS,
  TRUST_CARD_TYPE,
  type BidEvent,
  type Lot,
  type Money,
  type RankingItem,
} from '../../../shared/api/types';
import { auctionRoomReducer, createInitialAuctionRoomState } from './auctionRoomReducer';

function money(amount: number): Money {
  return { amount, currency: 'CNY' };
}

function lot(overrides: Partial<Lot> = {}): Lot {
  return {
    id: 'lot-1',
    roomId: 'room-1',
    title: '测试拍品',
    status: LOT_STATUS.LIVE,
    currentPrice: money(100),
    stats: { participantCount: 1, bidCount: 1 },
    rule: {
      startPrice: money(0),
      minIncrement: money(50),
      durationSeconds: 300,
      antiSnipeWindowSeconds: 10,
      antiSnipeExtendSeconds: 15,
    },
    version: 1,
    ...overrides,
  };
}

const ranking: RankingItem[] = [
  { userId: 'buyer-1', nickname: '买家一', amount: money(100), rank: 1 },
];

const bid: BidEvent = {
  id: 'bid-1',
  lotId: 'lot-1',
  userId: 'buyer-1',
  nickname: '买家一',
  amount: money(100),
  accepted: true,
};

describe('auctionRoomReducer', () => {
  it('applies room snapshots as the baseline state', () => {
    const state = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'snapshotReceived',
      snapshot: {
        roomId: 'room-1',
        currentLot: lot(),
        ranking,
        recentBids: [bid],
        serverTimeUnixMs: 1000,
      },
    });

    expect(state.currentLot?.id).toBe('lot-1');
    expect(state.ranking).toHaveLength(1);
    expect(state.recentBids[0]?.id).toBe('bid-1');
    expect(state.eventState.source).toBe('snapshot');
  });

  it('updates lot, ranking, recent bids and clears pending bid on accepted bids', () => {
    const pending = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'localBidStarted',
      lotId: 'lot-1',
      amount: money(150),
      idempotencyKey: 'idem-1',
    });

    const state = auctionRoomReducer(pending, {
      type: 'eventReceived',
      event: {
        id: 'event-1',
        type: AUCTION_EVENT_TYPE.BID_ACCEPTED,
        lot: lot({ currentPrice: money(150), version: 2 }),
        ranking: [{ userId: 'buyer-2', nickname: '买家二', amount: money(150), rank: 1 }],
        bid: { ...bid, id: 'bid-2', userId: 'buyer-2', amount: money(150) },
      },
    });

    expect(state.currentLot?.currentPrice).toEqual(money(150));
    expect(state.ranking[0]?.userId).toBe('buyer-2');
    expect(state.recentBids[0]?.id).toBe('bid-2');
    expect(state.localOptimistic.pendingBid).toBeUndefined();
  });

  it('keeps existing lot media when bid events only include realtime fields', () => {
    const base = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'snapshotReceived',
      snapshot: {
        roomId: 'room-1',
        currentLot: lot({
          imageUrl: 'https://cdn.example.com/lot-1.jpg',
          galleryImageUrls: ['https://cdn.example.com/gallery-a.jpg', 'https://cdn.example.com/gallery-b.jpg'],
          description: '完整商品描述',
          trustCards: [{
            id: 'card-1',
            type: TRUST_CARD_TYPE.CERTIFICATE,
            title: '证书',
            imageUrl: 'https://cdn.example.com/cert-1.jpg',
          }],
        }),
        ranking,
        recentBids: [],
      },
    });

    const state = auctionRoomReducer(base, {
      type: 'eventReceived',
      event: {
        type: AUCTION_EVENT_TYPE.BID_ACCEPTED,
        lot: lot({
          currentPrice: money(200),
          imageUrl: '',
          galleryImageUrls: [],
          description: '',
          trustCards: [],
          version: 2,
        }),
        ranking: [{ userId: 'buyer-2', nickname: '买家二', amount: money(200), rank: 1 }],
        bid: { ...bid, id: 'bid-2', userId: 'buyer-2', amount: money(200) },
      },
    });

    expect(state.currentLot?.currentPrice).toEqual(money(200));
    expect(state.currentLot?.imageUrl).toBe('https://cdn.example.com/lot-1.jpg');
    expect(state.currentLot?.galleryImageUrls).toEqual(['https://cdn.example.com/gallery-a.jpg', 'https://cdn.example.com/gallery-b.jpg']);
    expect(state.currentLot?.description).toBe('完整商品描述');
    expect(state.currentLot?.trustCards?.[0]?.imageUrl).toBe('https://cdn.example.com/cert-1.jpg');
  });

  it('replaces ranking and lot state from ranking updated events', () => {
    const state = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'eventReceived',
      event: {
        type: AUCTION_EVENT_TYPE.RANKING_UPDATED,
        lot: lot({ currentPrice: money(200), leadingUserId: 'buyer-3', version: 2 }),
        ranking: [{ userId: 'buyer-3', nickname: '买家三', amount: money(200), rank: 1 }],
      },
    });

    expect(state.currentLot).toEqual(expect.objectContaining({ currentPrice: money(200), leadingUserId: 'buyer-3', version: 2 }));
    expect(state.ranking).toEqual([{ userId: 'buyer-3', nickname: '买家三', amount: money(200), rank: 1 }]);
  });

  it('replaces ranking from lot update events emitted by realtime V1', () => {
    const state = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'eventReceived',
      event: {
        type: AUCTION_EVENT_TYPE.LOT_UPDATED,
        lot: lot({ currentPrice: money(250), version: 3 }),
        ranking: [{ userId: '', nickname: '买***四', amount: money(250), rank: 1 }],
      },
    });

    expect(state.currentLot?.currentPrice).toEqual(money(250));
    expect(state.ranking).toEqual([{ userId: '', nickname: '买***四', amount: money(250), rank: 1 }]);
  });

  it('clears pending bid and marks the lot cancelled', () => {
    const pending = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'localBidStarted',
      lotId: 'lot-1',
      amount: money(150),
      idempotencyKey: 'idem-1',
    });

    const state = auctionRoomReducer(pending, {
      type: 'eventReceived',
      event: {
        type: AUCTION_EVENT_TYPE.LOT_CANCELLED,
        lot: lot({ status: LOT_STATUS.CANCELLED, cancelReason: '主播异常取消' }),
      },
    });

    expect(state.currentLot?.status).toBe(LOT_STATUS.CANCELLED);
    expect(state.currentLot?.cancelReason).toBe('主播异常取消');
    expect(state.localOptimistic.pendingBid).toBeUndefined();
  });

  it('applies settled and closed lot events', () => {
    const settled = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'eventReceived',
      event: {
        type: AUCTION_EVENT_TYPE.LOT_SETTLED,
        lot: lot({ status: LOT_STATUS.SETTLED, finalPrice: money(300), winnerUserId: 'buyer-1' }),
      },
    });

    expect(settled.currentLot?.status).toBe(LOT_STATUS.SETTLED);
    expect(settled.currentLot?.finalPrice).toEqual(money(300));

    const closed = auctionRoomReducer(settled, {
      type: 'eventReceived',
      event: {
        type: AUCTION_EVENT_TYPE.AUCTION_CLOSED,
        lot: lot({ status: LOT_STATUS.SETTLED, finalPrice: money(300), winnerUserId: 'buyer-1' }),
      },
    });

    expect(closed.currentLot?.winnerUserId).toBe('buyer-1');
  });

  it('applies extended auction lots without losing current ranking', () => {
    const base = auctionRoomReducer(createInitialAuctionRoomState('room-1'), {
      type: 'snapshotReceived',
      snapshot: { roomId: 'room-1', currentLot: lot(), ranking, recentBids: [] },
    });

    const state = auctionRoomReducer(base, {
      type: 'eventReceived',
      event: {
        type: AUCTION_EVENT_TYPE.AUCTION_EXTENDED,
        lot: lot({ status: LOT_STATUS.EXTENDED, endsAtUnixMs: 2000, version: 2 }),
      },
    });

    expect(state.currentLot?.status).toBe(LOT_STATUS.EXTENDED);
    expect(state.currentLot?.endsAtUnixMs).toBe(2000);
    expect(state.ranking).toEqual(ranking);
  });
});
