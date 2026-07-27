import type { AdminUsersQuery } from '../../features/admin-user/api/adminUserApi';
import type { AdminLotsQuery } from '../../features/auction/api/auctionApi';
import type { AdminOrdersQuery } from '../../features/order/api/orderApi';

export const adminQueryKeys = {
  all: ['admin'] as const,
  rooms: () => ['admin', 'rooms'] as const,
  dashboard: (roomId: string) => ['admin', 'dashboard', roomId] as const,
  liveControl: (roomId: string) => ['admin', 'live-control', roomId] as const,
  lots: () => ['admin', 'lots'] as const,
  lotList: (query: AdminLotsQuery) => ['admin', 'lots', 'list', query] as const,
  roomSnapshot: (roomId: string) => ['admin', 'rooms', roomId, 'snapshot'] as const,
  orders: () => ['admin', 'orders'] as const,
  orderList: (query: AdminOrdersQuery) => ['admin', 'orders', 'list', query] as const,
  lotResult: (lotId: string) => ['admin', 'lots', lotId, 'result'] as const,
  users: () => ['admin', 'users'] as const,
  userList: (query: AdminUsersQuery) => ['admin', 'users', 'list', query] as const,
};
