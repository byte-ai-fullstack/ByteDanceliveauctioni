import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useMemo, useState } from 'react';
import { getRoomSnapshot, listAdminLots, type AdminLotsQuery } from '../../auction/api/auctionApi';
import { adminQueryKeys } from '../../../shared/api/queryKeys';
import { resultMessage } from '../../../shared/api/result';

export function useAuctionManagementPage(roomId: string, pageSize: number) {
  const queryClient = useQueryClient();
  const initialQuery = { page: 1, pageSize, roomId, view: 'current' as const };
  const [query, setQuery] = useState<AdminLotsQuery>(initialQuery);
  const [requestQuery, setRequestQuery] = useState<AdminLotsQuery>(initialQuery);
  const activeRequestQuery = useMemo(() => ({ ...requestQuery, roomId, view: 'current' as const, pageSize }), [pageSize, requestQuery, roomId]);

  const lotsQuery = useQuery({
    queryKey: adminQueryKeys.lotList(activeRequestQuery),
    queryFn: async ({ signal }) => {
      const [page, snapshot] = await Promise.all([
        listAdminLots(activeRequestQuery, signal),
        getRoomSnapshot(roomId, signal),
      ]);
      return { ...page, snapshot };
    },
    placeholderData: keepPreviousData,
  });

  const syncLots = useCallback(async (nextQuery = query) => {
    const target = { ...nextQuery, roomId, view: 'current' as const, pageSize, page: nextQuery.page ?? 1 };
    setQuery(target);
    if (sameQuery(target, activeRequestQuery)) {
      await queryClient.invalidateQueries({ queryKey: adminQueryKeys.lotList(activeRequestQuery), exact: true });
      return;
    }
    setRequestQuery(target);
  }, [activeRequestQuery, pageSize, query, queryClient, roomId]);

  const updateQuery = (patch: Partial<AdminLotsQuery>) => {
    const next = { ...query, ...patch, roomId, view: 'current' as const, page: patch.page ?? 1 };
    setQuery(next);
    if (patch.status !== undefined || patch.page !== undefined) setRequestQuery(next);
  };

  const recoverSnapshot = useCallback(() => queryClient.fetchQuery({
    queryKey: adminQueryKeys.roomSnapshot(roomId),
    queryFn: ({ signal }) => getRoomSnapshot(roomId, signal),
    staleTime: 0,
  }), [queryClient, roomId]);

  const page = lotsQuery.data;
  const total = page?.total ?? 0;
  const currentPage = page?.page ?? query.page ?? 1;

  return {
    query: { ...query, roomId, view: 'current' as const, pageSize },
    lots: page?.lots ?? [],
    total,
    snapshot: page?.snapshot ?? null,
    loading: lotsQuery.isFetching,
    error: lotsQuery.error ? resultMessage(lotsQuery.error) : '',
    totalPages: Math.max(1, Math.ceil(total / pageSize)),
    currentPage,
    syncLots,
    updateQuery,
    goPrevPage: () => updateQuery({ page: Math.max(1, currentPage - 1) }),
    goNextPage: () => updateQuery({ page: currentPage + 1 }),
    recoverSnapshot,
  };
}

function sameQuery(a: AdminLotsQuery, b: AdminLotsQuery) {
  return a.page === b.page
    && a.pageSize === b.pageSize
    && a.status === b.status
    && a.view === b.view
    && a.keyword === b.keyword
    && a.roomId === b.roomId;
}
