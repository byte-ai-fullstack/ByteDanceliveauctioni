// @vitest-environment jsdom
import { render, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { UserOrder } from '../features/shop/api/shopApi';
import { PERMISSION_CODE, ROLE_CODE, USER_STATUS, type AuthTokens, type User } from '../shared/api/types';
import type { AuthSessionContextValue } from '../shared/auth/authSessionContext';
import { serializeMarkup, SNAPSHOT_NOW_MS } from '../test/domSnapshot';
import { ShopOrdersPage } from './ShopOrdersPage';

const dependencies = vi.hoisted(() => ({
  auth: null as AuthSessionContextValue | null,
  listUserOrders: vi.fn(),
  listMyFrequentStores: vi.fn(),
  mockPayUserOrder: vi.fn(),
}));

vi.mock('../shared/auth/useAuthSession', () => ({
  useAuthSession: () => {
    if (!dependencies.auth) throw new Error('订单快照缺少认证 fixture');
    return dependencies.auth;
  },
}));

vi.mock('../features/shop/api/shopApi', async (importOriginal) => {
  const original = await importOriginal<typeof import('../features/shop/api/shopApi')>();
  return {
    ...original,
    listUserOrders: dependencies.listUserOrders,
    listMyFrequentStores: dependencies.listMyFrequentStores,
    mockPayUserOrder: dependencies.mockPayUserOrder,
  };
});

const buyer: User = {
  id: 'user-me',
  username: 'snapshot-buyer',
  nickname: '我',
  roleCodes: [ROLE_CODE.BUYER],
  permissionCodes: [PERMISSION_CODE.BID_PLACE],
  mainAccountId: 'user-me',
  createdByUserId: 'user-me',
  status: USER_STATUS.ACTIVE,
  createdAtUnixMs: SNAPSHOT_NOW_MS - 86_400_000,
  updatedAtUnixMs: SNAPSHOT_NOW_MS - 1_000,
};

const tokens: AuthTokens = {
  accessToken: 'snapshot-access-token',
  refreshToken: 'snapshot-refresh-token',
  accessExpiresAtUnixMs: SNAPSHOT_NOW_MS + 3_600_000,
  refreshExpiresAtUnixMs: SNAPSHOT_NOW_MS + 86_400_000,
};

function authenticatedSession(): AuthSessionContextValue {
  const session = { user: buyer, tokens };
  return {
    ...session,
    status: 'authenticated',
    authMode: 'real',
    ensureBuyerSession: vi.fn(async () => session),
    ensureReadyForBid: vi.fn(async () => session),
    loginBuyer: vi.fn(async () => session),
    registerBuyer: vi.fn(async () => session),
    resetBuyerPassword: vi.fn(async () => buyer),
    refreshIfNeeded: vi.fn(async () => true),
    logout: vi.fn(async () => {}),
  };
}

const order: UserOrder = {
  id: 'order-fixture-01',
  source: 'auction',
  sourceOrderId: 'auction-order-fixture-01',
  orderNo: 'AUCTION-20260726-001',
  mainAccountId: 'user-me',
  userId: 'user-me',
  nickname: '我',
  status: 'pending_payment',
  paymentStatus: 'init',
  title: '和田玉平安扣',
  shopName: '严选珠宝直播间',
  totalAmount: 72_000,
  currency: 'CNY',
  createdAtUnixMs: SNAPSHOT_NOW_MS - 5_000,
  updatedAtUnixMs: SNAPSHOT_NOW_MS - 5_000,
  expiresAtUnixMs: SNAPSHOT_NOW_MS + 900_000,
  items: [{
    id: 'order-item-fixture-01',
    orderId: 'order-fixture-01',
    source: 'auction',
    lotId: 'lot-fixture-01',
    roomId: 'room-fixture-01',
    title: '和田玉平安扣',
    imageUrl: 'https://assets.example.test/lot-fixture-01.jpg',
    skuName: '直播拍得',
    quantity: 1,
    unitAmount: 72_000,
    totalAmount: 72_000,
    currency: 'CNY',
  }],
};

beforeEach(() => {
  window.history.replaceState({}, '', '/shop/orders?from=result');
  dependencies.auth = authenticatedSession();
  dependencies.listUserOrders.mockResolvedValue({
    orders: [order],
    total: 1,
    page: 1,
    pageSize: 50,
  });
  dependencies.listMyFrequentStores.mockResolvedValue({
    stores: [{
      storeKey: 'auction:room-fixture-01',
      storeName: '严选珠宝直播间',
      source: 'auction',
      orderCount: 3,
      lastOrderAtUnixMs: SNAPSHOT_NOW_MS - 5_000,
      imageUrl: 'https://assets.example.test/store-fixture-01.jpg',
      targetUrl: '/m/room/room-fixture-01',
    }],
    total: 1,
    limit: 10,
  });
});

describe('ShopOrdersPage 表现冻结', () => {
  it('展示已登录买家的待支付订单', async () => {
    const { container } = render(<ShopOrdersPage />);

    await waitFor(() => {
      expect(container.textContent).toContain('严选珠宝直播间');
      expect(container.textContent).toContain('和田玉平安扣');
      expect(container.textContent).toContain('去支付');
    });

    await expect(serializeMarkup(container)).toMatchFileSnapshot('./__dom__/shop-orders-pending.html');
  });
});
