// @vitest-environment jsdom
import { cleanup } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PERMISSION_CODE, ROLE_CODE, USER_STATUS, type Room, type RoomSnapshot, type User } from '../../shared/api/types';
import { SNAPSHOT_NOW_MS, renderSettledMarkup } from '../../test/domSnapshot';
import { createAdminQueryClient } from '../../shared/api/queryClient';
import { HostConsolePage } from './HostConsolePage';

const apiMocks = vi.hoisted(() => ({
  listAdminRooms: vi.fn(),
  listAdminLots: vi.fn(),
  getRoomSnapshot: vi.fn(),
  createDraftLot: vi.fn(),
  patchDraftLot: vi.fn(),
  queueLot: vi.fn(),
  uploadImage: vi.fn(),
  deleteUploadedImage: vi.fn(),
  startLot: vi.fn(),
  startDuel: vi.fn(),
  settleLot: vi.fn(),
  cancelLot: vi.fn(),
  revealTrustCard: vi.fn(),
  listAdminOrders: vi.fn(),
  getLotResult: vi.fn(),
  listAdminUsers: vi.fn(),
  adminCreateUser: vi.fn(),
  adminResetUserPassword: vi.fn(),
  adminUpdateUserRole: vi.fn(),
  adminUpdateUserStatus: vi.fn(),
  currentAuth: vi.fn(),
}));

vi.mock('../../features/auction/api/auctionApi', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../features/auction/api/auctionApi')>(),
  listAdminRooms: apiMocks.listAdminRooms,
  listAdminLots: apiMocks.listAdminLots,
  getRoomSnapshot: apiMocks.getRoomSnapshot,
  createDraftLot: apiMocks.createDraftLot,
  patchDraftLot: apiMocks.patchDraftLot,
  queueLot: apiMocks.queueLot,
  uploadImage: apiMocks.uploadImage,
  deleteUploadedImage: apiMocks.deleteUploadedImage,
  startLot: apiMocks.startLot,
  startDuel: apiMocks.startDuel,
  settleLot: apiMocks.settleLot,
  cancelLot: apiMocks.cancelLot,
  revealTrustCard: apiMocks.revealTrustCard,
}));

vi.mock('../../features/order/api/orderApi', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../features/order/api/orderApi')>(),
  listAdminOrders: apiMocks.listAdminOrders,
  getLotResult: apiMocks.getLotResult,
}));

vi.mock('../../features/admin-user/api/adminUserApi', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../features/admin-user/api/adminUserApi')>(),
  listAdminUsers: apiMocks.listAdminUsers,
  adminCreateUser: apiMocks.adminCreateUser,
  adminResetUserPassword: apiMocks.adminResetUserPassword,
  adminUpdateUserRole: apiMocks.adminUpdateUserRole,
  adminUpdateUserStatus: apiMocks.adminUpdateUserStatus,
}));

vi.mock('../../features/auth/api/authApi', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../features/auth/api/authApi')>(),
  currentAuth: apiMocks.currentAuth,
}));

vi.mock('../../shared/realtime/useRoomSocket', () => ({
  roomSocketStatusLabel: (status: string) => status === 'connected' ? '已连接' : '连接中',
  useRoomSocket: () => ({
    status: 'connected',
    reconnectCount: 0,
    lastEventAt: null,
    lastEventAtText: '未收到',
    lastEventType: '暂无',
    lastLotVersion: 0,
  }),
}));

const fixtureRoom: Room = {
  id: 'room-fixture',
  mainAccountId: 'merchant-fixture',
  name: '测试直播间',
  platform: 'douyin',
  status: 'ACTIVE',
  createdAtUnixMs: SNAPSHOT_NOW_MS - 86_400_000,
  updatedAtUnixMs: SNAPSHOT_NOW_MS,
};

const fixtureSnapshot: RoomSnapshot = {
  roomId: fixtureRoom.id,
  ranking: [],
  recentBids: [],
  playbookStage: 'PLAYBOOK_STAGE_WARM_UP',
  serverTimeUnixMs: SNAPSHOT_NOW_MS,
};

const fixtureUser: User = {
  id: 'merchant-owner-fixture',
  username: 'merchant-owner',
  nickname: '测试主账号',
  roleCodes: [ROLE_CODE.MERCHANT_OWNER],
  permissionCodes: Object.values(PERMISSION_CODE),
  mainAccountId: 'merchant-fixture',
  createdByUserId: 'system',
  status: USER_STATUS.ACTIVE,
  createdAtUnixMs: SNAPSHOT_NOW_MS - 86_400_000,
  updatedAtUnixMs: SNAPSHOT_NOW_MS,
};

const routes = [
  { path: '/admin', snapshot: 'dashboard.html' },
  { path: '/admin/auctions/current/control', snapshot: 'live-control.html' },
  { path: '/admin/auctions/create', snapshot: 'auction-create.html' },
  { path: '/admin/auctions', snapshot: 'auction-queue.html' },
  { path: '/admin/auctions/history', snapshot: 'auction-history.html' },
  { path: '/admin/orders', snapshot: 'orders.html' },
  { path: '/admin/bids', snapshot: 'bid-audit.html' },
  { path: '/admin/merchants', snapshot: 'team-accounts.html' },
  { path: '/admin/realtime', snapshot: 'realtime-diagnostics.html' },
  { path: '/admin/settings', snapshot: 'settings.html' },
  { path: '/admin/alerts', snapshot: 'alerts.html' },
] as const;

let dateNowSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(SNAPSHOT_NOW_MS);
  dateNowSpy = vi.spyOn(Date, 'now').mockReturnValue(SNAPSHOT_NOW_MS);
  apiMocks.listAdminRooms.mockResolvedValue([fixtureRoom]);
  apiMocks.listAdminLots.mockImplementation(async (query: { page?: number; pageSize?: number } = {}) => ({
    lots: [],
    total: 0,
    page: query.page ?? 1,
    pageSize: query.pageSize ?? 20,
  }));
  apiMocks.getRoomSnapshot.mockResolvedValue(fixtureSnapshot);
  apiMocks.listAdminOrders.mockImplementation(async (query: { page?: number; pageSize?: number } = {}) => ({
    orders: [],
    total: 0,
    page: query.page ?? 1,
    pageSize: query.pageSize ?? 20,
  }));
  apiMocks.listAdminUsers.mockImplementation(async (query: { page?: number; pageSize?: number } = {}) => ({
    users: [],
    total: 0,
    page: query.page ?? 1,
    pageSize: query.pageSize ?? 20,
  }));
  apiMocks.getLotResult.mockResolvedValue({ auctionState: 'UNSPECIFIED' });
  apiMocks.currentAuth.mockReturnValue({ user: fixtureUser, tokens: null });
});

afterEach(() => {
  dateNowSpy.mockRestore();
  cleanup();
  vi.useRealTimers();
  window.history.replaceState({}, '', '/');
});

describe('后台管理路由表现冻结', () => {
  for (const route of routes) {
    it(route.path, async () => {
      window.history.replaceState({}, '', route.path);
      const markup = await renderSettledMarkup(
        <QueryClientProvider client={createAdminQueryClient()}><HostConsolePage /></QueryClientProvider>,
        '.laAdminShell',
      );
      await expect(markup).toMatchFileSnapshot(`./__dom__/${route.snapshot}`);
    });
  }
});
