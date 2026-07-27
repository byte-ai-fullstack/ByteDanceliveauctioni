import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';
import { getRoomSnapshot, listAdminLots } from '../../auction/api/auctionApi';
import { listAdminOrders } from '../../order/api/orderApi';
import { adminQueryKeys } from '../../../shared/api/queryKeys';
import { resultMessage } from '../../../shared/api/result';

const MAX_PAGE_COUNT = 5;
const PAGE_SIZE = 100;

export function useAdminDashboardPage(roomId: string) {
  const queryClient = useQueryClient();
  const dashboardQuery = useQuery({
    queryKey: adminQueryKeys.dashboard(roomId),
    queryFn: async ({ signal }) => {
      const [snapshot, lots, orders] = await Promise.all([
        getRoomSnapshot(roomId, signal),
        fetchAllLots(roomId, signal),
        fetchMerchantOrders(roomId, signal),
      ]);
      return { snapshot, lots, orders };
    },
    placeholderData: keepPreviousData,
  });

  const sync = useCallback(async () => {
    await dashboardQuery.refetch({ throwOnError: true });
  }, [dashboardQuery]);
  const recoverSnapshot = useCallback(() => queryClient.fetchQuery({
    queryKey: adminQueryKeys.roomSnapshot(roomId),
    queryFn: ({ signal }) => getRoomSnapshot(roomId, signal),
    staleTime: 0,
  }), [queryClient, roomId]);

  return {
    snapshot: dashboardQuery.data?.snapshot ?? null,
    lots: dashboardQuery.data?.lots ?? [],
    orders: dashboardQuery.data?.orders ?? [],
    loading: dashboardQuery.isFetching,
    error: dashboardQuery.error ? resultMessage(dashboardQuery.error) : '',
    lastUpdatedAt: dashboardQuery.dataUpdatedAt,
    refreshSeq: dashboardQuery.dataUpdatedAt,
    sync,
    recoverSnapshot,
  };
}

async function fetchAllLots(roomId: string, signal: AbortSignal) {
  const first = await listAdminLots({ page: 1, pageSize: PAGE_SIZE, roomId }, signal);
  const pageCount = Math.min(MAX_PAGE_COUNT, Math.ceil(first.total / PAGE_SIZE));
  if (pageCount <= 1) return first.lots;
  const pages = await Promise.all(Array.from({ length: pageCount - 1 }, (_, index) => listAdminLots({ page: index + 2, pageSize: PAGE_SIZE, roomId }, signal)));
  return [first, ...pages].flatMap((page) => page.lots);
}

async function fetchMerchantOrders(roomId: string, signal: AbortSignal) {
  const first = await listAdminOrders({ page: 1, pageSize: PAGE_SIZE }, signal);
  const pageCount = Math.min(MAX_PAGE_COUNT, Math.ceil(first.total / PAGE_SIZE));
  const pages = pageCount <= 1 ? [] : await Promise.all(Array.from({ length: pageCount - 1 }, (_, index) => listAdminOrders({ page: index + 2, pageSize: PAGE_SIZE }, signal)));
  const allOrders = [first, ...pages].flatMap((page) => page.orders);
  const roomOrders = allOrders.filter((order) => order.roomId === roomId);
  return roomOrders.length || !allOrders.length ? roomOrders : allOrders;
}
