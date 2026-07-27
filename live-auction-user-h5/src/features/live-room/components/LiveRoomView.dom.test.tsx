// @vitest-environment jsdom
/**
 * 表现冻结快照（G2）—— 直播间主视图。
 *
 * 快照文件 `__dom__/*.html` 是 committed 基线。重构期间它们必须保持零 diff；
 * 出现 diff 就意味着大表现被改动了，除非产品明确要求，否则应视为回归。
 *
 * 见 docs/Refactor/H5_TARGET_BLUEPRINT.md 第 2 节。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LOT_STATUS } from '../../../shared/api/types';
import { SNAPSHOT_NOW_MS, renderMarkup } from '../../../test/domSnapshot';
import { fixtureLiveRoomController, fixtureLot, fixtureOrder } from '../../../test/fixtures/liveRoomController';
import { LiveRoomView } from './LiveRoomView';

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: false });
  vi.setSystemTime(SNAPSHOT_NOW_MS);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('LiveRoomView 表现冻结', () => {
  it('竞拍进行中', async () => {
    const markup = renderMarkup(
      <LiveRoomView controller={fixtureLiveRoomController()} />,
    );
    await expect(markup).toMatchFileSnapshot('./__dom__/live-room-live.html');
  });

  it('已成交', async () => {
    const lot = fixtureLot({
      status: LOT_STATUS.SETTLED,
      winnerUserId: 'user-me',
      winnerNickname: '我',
      finalPrice: { amount: 72_000, currency: 'CNY' },
      settledAtUnixMs: SNAPSHOT_NOW_MS - 2_000,
      endsAtUnixMs: SNAPSHOT_NOW_MS - 2_000,
    });
    const markup = renderMarkup(
      <LiveRoomView
        controller={fixtureLiveRoomController({
          currentLot: lot,
          resultLot: lot,
          visibleResultOrder: fixtureOrder(),
        })}
      />,
    );
    await expect(markup).toMatchFileSnapshot('./__dom__/live-room-settled.html');
  });

  it('主播取消', async () => {
    const lot = fixtureLot({
      status: LOT_STATUS.CANCELLED,
      cancelReason: '商品信息有误，主播已取消本件拍品',
      cancelledAtUnixMs: SNAPSHOT_NOW_MS - 3_000,
      endsAtUnixMs: SNAPSHOT_NOW_MS - 3_000,
    });
    const markup = renderMarkup(
      <LiveRoomView
        controller={fixtureLiveRoomController({ currentLot: lot, resultLot: lot })}
      />,
    );
    await expect(markup).toMatchFileSnapshot('./__dom__/live-room-cancelled.html');
  });

  it('加载中与加载失败', async () => {
    const loading = renderMarkup(
      <LiveRoomView
        controller={fixtureLiveRoomController({ loading: true, currentLot: null })}
      />,
    );
    await expect(loading).toMatchFileSnapshot('./__dom__/live-room-loading.html');

    const failed = renderMarkup(
      <LiveRoomView
        controller={fixtureLiveRoomController({ error: '房间状态加载失败', currentLot: null })}
      />,
    );
    await expect(failed).toMatchFileSnapshot('./__dom__/live-room-error.html');
  });

  it('未登录买家与出价失败提示', async () => {
    const markup = renderMarkup(
      <LiveRoomView
        controller={fixtureLiveRoomController({
          meId: '',
          showBuyerAuth: true,
          bidAuthPanelOpen: true,
          bidError: '出价金额太低，请按当前加价幅度重新出价',
          notices: ['你已被超越，快加价'],
        })}
      />,
    );
    await expect(markup).toMatchFileSnapshot('./__dom__/live-room-auth-required.html');
  });

  it('竞拍面板展开', async () => {
    const markup = renderMarkup(
      <LiveRoomView
        controller={fixtureLiveRoomController({
          auctionPanel: {
            open: true,
            tab: 'queue',
            lots: [fixtureLot(), fixtureLot({ id: 'lot-fixture-02', title: '碧玉手镯', status: LOT_STATUS.QUEUED })],
            loading: false,
            error: '',
          },
        })}
      />,
    );
    await expect(markup).toMatchFileSnapshot('./__dom__/live-room-panel-queue.html');
  });
});
