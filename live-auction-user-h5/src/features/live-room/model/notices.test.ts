import { describe, expect, it } from 'vitest';
import { AUCTION_EVENT_TYPE, LOT_STATUS, type AuctionSocketEvent, type Lot, type Money } from '../../../shared/api/types';
import { noticeForAuctionEvent } from './notices';

function money(amount: number): Money {
  return { amount, currency: 'CNY' };
}

function lot(overrides: Partial<Lot> = {}): Lot {
  return {
    id: 'lot-1',
    roomId: 'room-1',
    title: '测试拍品',
    status: LOT_STATUS.LIVE,
    currentPrice: money(20000),
    stats: { participantCount: 2, bidCount: 2 },
    rule: {
      startPrice: money(0),
      minIncrement: money(5000),
      durationSeconds: 300,
      antiSnipeWindowSeconds: 10,
      antiSnipeExtendSeconds: 15,
    },
    leadingUserId: 'buyer-1',
    version: 1,
    ...overrides,
  };
}

function event(overrides: Partial<AuctionSocketEvent>): AuctionSocketEvent {
  return {
    type: AUCTION_EVENT_TYPE.BID_ACCEPTED,
    roomId: 'room-1',
    lotId: 'lot-1',
    ...overrides,
  };
}

describe('noticeForAuctionEvent', () => {
  it('shows a confirmation for the current user bid', () => {
    expect(noticeForAuctionEvent(event({
      lot: lot({ leadingUserId: 'buyer-1' }),
      bid: { userId: 'buyer-1', amount: money(20000) },
    }), 'buyer-1')).toBe('出价已确认，当前价 200.00元');
  });

  it('suppresses non-current buyer bid noise', () => {
    expect(noticeForAuctionEvent(event({
      lot: lot({ leadingUserId: 'buyer-2' }),
      bid: { userId: 'buyer-2', nickname: 'X***', amount: money(20000) },
    }), 'buyer-1', 'buyer-3')).toBe('');
  });

  it('shows outbid notice when another accepted bid replaces the current user as leader', () => {
    expect(noticeForAuctionEvent(event({
      lot: lot({ leadingUserId: 'buyer-2' }),
      bid: { userId: 'buyer-2', nickname: 'X***', amount: money(20000) },
    }), 'buyer-1', 'buyer-1')).toBe('你已被超越，可继续加价');
  });

  it('only shows outbid notice when the current user lost the lead', () => {
    expect(noticeForAuctionEvent(event({
      type: AUCTION_EVENT_TYPE.BID_OUTBID,
      lot: lot({ leadingUserId: 'buyer-2' }),
    }), 'buyer-1', 'buyer-1')).toBe('你已被超越，可继续加价');

    expect(noticeForAuctionEvent(event({
      type: AUCTION_EVENT_TYPE.BID_OUTBID,
      lot: lot({ leadingUserId: 'buyer-2' }),
    }), 'buyer-1', 'buyer-3')).toBe('');
  });
});
