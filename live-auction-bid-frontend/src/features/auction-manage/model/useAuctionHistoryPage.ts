import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useMemo, useState } from 'react';
import { listAdminLots, type AdminLotsQuery } from '../../auction/api/auctionApi';
import { getLotResult } from '../../order/api/orderApi';
import { isSettlementLot } from '../../../entities/auction/model/auctionStatus';
import type { OrderSummary } from '../../../entities/order/model/orderTypes';
import type { Lot } from '../../../shared/api/types';
import { adminQueryKeys } from '../../../shared/api/queryKeys';
import { resultMessage } from '../../../shared/api/result';

export function useAuctionHistoryPage(roomId: string, pageSize: number) {
  const queryClient = useQueryClient();
  const initialQuery = { page: 1, pageSize, roomId, view: 'history' as const };
  const [query, setQuery] = useState<AdminLotsQuery>(initialQuery);
  const [requestQuery, setRequestQuery] = useState<AdminLotsQuery>(initialQuery);
  const activeRequestQuery = useMemo(() => ({ ...requestQuery, roomId, view: 'history' as const, pageSize }), [pageSize, requestQuery, roomId]);

  const historyQuery = useQuery({
    queryKey: adminQueryKeys.lotList(activeRequestQuery),
    queryFn: async ({ signal }) => {
      const page = await listAdminLots(activeRequestQuery, signal);
      return { ...page, ordersByLotId: await loadHistoryOrders(page.lots, signal) };
    },
    placeholderData: keepPreviousData,
  });

  const syncLots = useCallback(async (nextQuery = query) => {
    const target = { ...nextQuery, roomId, view: 'history' as const, pageSize, page: nextQuery.page ?? 1 };
    setQuery(target);
    if (sameQuery(target, activeRequestQuery)) {
      await queryClient.invalidateQueries({ queryKey: adminQueryKeys.lotList(activeRequestQuery), exact: true });
      return;
    }
    setRequestQuery(target);
  }, [activeRequestQuery, pageSize, query, queryClient, roomId]);

  const updateQuery = (patch: Partial<AdminLotsQuery>) => {
    const next = { ...query, ...patch, roomId, view: 'history' as const, page: patch.page ?? 1 };
    setQuery(next);
    if (patch.status !== undefined || patch.page !== undefined) setRequestQuery(next);
  };
  const goPrevPage = () => updateQuery({ page: Math.max(1, (query.page || 1) - 1) });
  const goNextPage = () => updateQuery({ page: (query.page || 1) + 1 });

  const page = historyQuery.data;
  const total = page?.total ?? 0;
  const currentPage = page?.page ?? query.page ?? 1;

  return {
    query: { ...query, roomId, view: 'history' as const, pageSize },
    lots: page?.lots ?? [],
    ordersByLotId: page?.ordersByLotId ?? {},
    total,
    loading: historyQuery.isFetching,
    error: historyQuery.error ? resultMessage(historyQuery.error) : '',
    totalPages: Math.max(1, Math.ceil(total / pageSize)),
    currentPage,
    goPrevPage,
    goNextPage,
    syncLots,
    updateQuery,
  };
}

async function loadHistoryOrders(lots: Lot[], signal: AbortSignal) {
  const settlementLots = lots.filter(isSettlementLot);
  const entries = await Promise.all(settlementLots.map(async (lot) => {
    try {
      const result = await getLotResult(lot.id, signal);
      return [lot.id, result.order ?? null] as const;
    } catch (error) {
      if (signal.aborted) throw error;
      return [lot.id, null] as const;
    }
  }));
  return Object.fromEntries(entries) as Record<string, OrderSummary | null>;
}

function sameQuery(a: AdminLotsQuery, b: AdminLotsQuery) {
  return a.page === b.page
    && a.pageSize === b.pageSize
    && a.status === b.status
    && a.view === b.view
    && a.keyword === b.keyword
    && a.roomId === b.roomId;
}
