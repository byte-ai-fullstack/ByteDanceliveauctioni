import { describe, expect, it } from 'vitest';
import { TRUST_CARD_TYPE } from './types';
import { normalizeLot } from './adapters';

describe('normalizeLot', () => {
  it('keeps uploaded certificate trust card images', () => {
    const lot = normalizeLot({
      id: 'lot-1',
      room_id: 'room-1',
      title: '和田玉平安扣',
      status: 'LOT_STATUS_LIVE',
      image_url: 'https://cdn.example.com/cover.jpg',
      gallery_image_urls: [' https://cdn.example.com/gallery-a.jpg ', '', 'https://cdn.example.com/gallery-b.jpg'],
      trust_cards: [
        {
          id: 'cert-1',
          lot_id: 'lot-1',
          type: 'TRUST_CARD_TYPE_CERTIFICATE',
          title: '鉴定证书',
          content: '天然和田玉检测报告',
          image_url: 'https://cdn.example.com/cert.jpg',
          revealed: true,
          revealed_at_unix_ms: 1710000000000,
        },
      ],
    });

    expect(lot.galleryImageUrls).toEqual([
      'https://cdn.example.com/gallery-a.jpg',
      'https://cdn.example.com/gallery-b.jpg',
    ]);
    expect(lot.trustCards).toEqual([
      {
        id: 'cert-1',
        lotId: 'lot-1',
        type: TRUST_CARD_TYPE.CERTIFICATE,
        title: '鉴定证书',
        content: '天然和田玉检测报告',
        imageUrl: 'https://cdn.example.com/cert.jpg',
        revealed: true,
        revealedAtUnixMs: 1710000000000,
      },
    ]);
  });

  it('defaults missing trust cards to an empty list', () => {
    const lot = normalizeLot({ id: 'lot-2', room_id: 'room-1', title: '无证书拍品' });

    expect(lot.trustCards).toEqual([]);
  });
});
