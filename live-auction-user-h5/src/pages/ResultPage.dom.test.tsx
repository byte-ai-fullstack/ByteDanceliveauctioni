// @vitest-environment jsdom
import { render, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { LOT_STATUS, type LotResult } from '../shared/api/types';
import { serializeMarkup } from '../test/domSnapshot';
import { fixtureLot } from '../test/fixtures/liveRoomController';
import { ResultPage } from './ResultPage';

const auctionApi = vi.hoisted(() => ({
  getLotResult: vi.fn(),
}));

vi.mock('../features/auction/api/auctionApi', () => ({
  getLotResult: auctionApi.getLotResult,
}));

beforeEach(() => {
  window.history.replaceState(
    {},
    '',
    '/m/result/lot-fixture-01?roomId=room-fixture-01&settledAt=2026-07-26%2023%3A00',
  );
  auctionApi.getLotResult.mockResolvedValue({
    lot: fixtureLot({
      status: LOT_STATUS.SETTLED,
      winnerUserId: 'user-me',
      winnerNickname: '我',
      finalPrice: { amount: 72_000, currency: 'CNY' },
      settledAtUnixMs: undefined,
    }),
    winnerUserId: 'user-me',
    winnerNickname: '我',
    finalPrice: { amount: 72_000, currency: 'CNY' },
  } satisfies LotResult);
});

describe('ResultPage 表现冻结', () => {
  it('展示服务器确认的成交结果', async () => {
    const { container } = render(<ResultPage />);

    await waitFor(() => {
      expect(container.textContent).toContain('服务器成交结果');
      expect(container.textContent).toContain('竞拍成功');
    });

    await expect(serializeMarkup(container)).toMatchFileSnapshot('./__dom__/result-server.html');
  });
});
