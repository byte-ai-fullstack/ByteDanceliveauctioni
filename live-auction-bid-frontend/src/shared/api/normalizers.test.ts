import { describe, expect, it } from 'vitest';
import {
  normalizeAuctionEvent,
  normalizeAuthTokens,
  normalizeLot,
  normalizeMoney,
  normalizeRoom,
  normalizeRoomPresence,
  normalizeRoomSnapshot,
  normalizeTrustRevealCard,
  normalizeUploadedAsset,
  normalizeUser,
} from './normalizers';

const lotFixture = {
  id: 'lot-1',
  roomId: 'room-1',
  title: '测试拍品',
  status: 'LOT_STATUS_LIVE',
  rule: {
    startPrice: { amount: '1000', currency: 'CNY' },
    minIncrement: { amount: 100, currency: 'CNY' },
    durationSeconds: 60,
  },
  currentPrice: { amount: 1200, currency: 'CNY' },
  trustCards: [],
  galleryImageUrls: [],
  tags: [],
  unknownFutureField: 'ignored',
};

describe('API response normalizers', () => {
  it('normalizes money values and defaults', () => {
    expect(normalizeMoney(undefined, 'money')).toEqual({ amount: 0, currency: 'CNY' });
    expect(normalizeMoney({ amount: '1200', currency: 'USD', ignored: true }, 'money')).toEqual({ amount: '1200', currency: 'USD' });
  });

  it('normalizes trust reveal cards', () => {
    expect(normalizeTrustRevealCard({ id: 'card-1', lotId: 'lot-1', type: 'TRUST_CARD_TYPE_DETAIL', title: '细节', revealed: true })).toMatchObject({
      id: 'card-1',
      lotId: 'lot-1',
      type: 'TRUST_CARD_TYPE_DETAIL',
      revealed: true,
      revealedAtUnixMs: 0,
    });
  });

  it('normalizes lots while tolerating unknown camelCase fields', () => {
    const lot = normalizeLot(lotFixture);
    expect(lot).toMatchObject({
      id: 'lot-1',
      roomId: 'room-1',
      status: 'LOT_STATUS_LIVE',
      currentPrice: { amount: 1200, currency: 'CNY' },
      stats: { participantCount: 0, bidCount: 0 },
    });
    expect(lot).not.toHaveProperty('unknownFutureField');
  });

  it('normalizes room snapshots with nested bids and ranking', () => {
    const snapshot = normalizeRoomSnapshot({
      roomId: 'room-1',
      currentLot: lotFixture,
      ranking: [{ rank: 1, userId: 'buyer-1', nickname: '买家', amount: { amount: 1200, currency: 'CNY' }, bidAtUnixMs: 100 }],
      recentBids: [{ id: 'bid-1', lotId: 'lot-1', userId: 'buyer-1', nickname: '买家', amount: { amount: 1200, currency: 'CNY' }, createdAtUnixMs: 100 }],
      playbookStage: 'PLAYBOOK_STAGE_BIDDING_ACTIVE',
      serverTimeUnixMs: '200',
    });
    expect(snapshot.currentLot?.id).toBe('lot-1');
    expect(snapshot.ranking[0]).toMatchObject({ userId: 'buyer-1', bidAtUnixMs: 100 });
    expect(snapshot.recentBids[0]).toMatchObject({ id: 'bid-1', createdAtUnixMs: 100 });
  });

  it('normalizes rooms and presence', () => {
    expect(normalizeRoom({ id: 'room-1', mainAccountId: 'merchant-1', name: '直播间', platform: 'douyin' })).toMatchObject({
      id: 'room-1',
      mainAccountId: 'merchant-1',
      status: 'ACTIVE',
    });
    expect(normalizeRoomPresence({ roomId: 'room-1', totalConnections: 3, viewerConnections: 2, operatorConnections: 1, serverTimeUnixMs: 200 })).toEqual({
      roomId: 'room-1',
      totalConnections: 3,
      viewerConnections: 2,
      operatorConnections: 1,
      serverTimeUnixMs: 200,
    });
  });

  it('normalizes auction events and nested entities', () => {
    const event = normalizeAuctionEvent({
      id: 'event-1',
      type: 'AUCTION_EVENT_TYPE_LOT_UPDATED',
      roomId: 'room-1',
      lotId: 'lot-1',
      occurredAtUnixMs: 300,
      lot: lotFixture,
      futureMetadata: { accepted: true },
    });
    expect(event).toMatchObject({ id: 'event-1', roomId: 'room-1', lotId: 'lot-1', occurredAtUnixMs: 300 });
    expect(event.lot?.id).toBe('lot-1');
    expect(event).toHaveProperty('futureMetadata');
  });

  it('normalizes users and auth tokens', () => {
    expect(normalizeUser({
      id: 'user-1',
      username: 'operator',
      nickname: '运营',
      roleCodes: ['operator'],
      permissionCodes: ['lot.view_admin'],
      mainAccountId: 'merchant-1',
      createdByUserId: 'owner-1',
      status: 'USER_STATUS_ACTIVE',
      createdAtUnixMs: 1,
      updatedAtUnixMs: 2,
    })).toMatchObject({ id: 'user-1', roleCodes: ['operator'], permissionCodes: ['lot.view_admin'] });
    expect(normalizeAuthTokens({ accessToken: 'a', refreshToken: 'r', accessExpiresAtUnixMs: 10, refreshExpiresAtUnixMs: 20 })).toEqual({
      accessToken: 'a',
      refreshToken: 'r',
      accessExpiresAtUnixMs: 10,
      refreshExpiresAtUnixMs: 20,
    });
  });

  it('normalizes uploaded assets', () => {
    expect(normalizeUploadedAsset({ id: 'asset-1', imageUrl: '/asset.jpg', bucket: 'images', objectKey: 'asset.jpg', mimeType: 'image/jpeg', sizeBytes: '42' })).toMatchObject({
      id: 'asset-1',
      imageUrl: '/asset.jpg',
      sizeBytes: '42',
    });
  });

  it('rejects legacy snake_case-only payloads', () => {
    expect(() => normalizeAuthTokens({ access_token: 'a', refresh_token: 'r', access_expires_at_unix_ms: 10, refresh_expires_at_unix_ms: 20 })).toThrow('response missing tokens.accessToken');
  });
});
