import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useState } from 'react';
import { getLotResult, listAdminOrders, type AdminOrdersQuery } from '../../order/api/orderApi';
import type { OrderSummary } from '../../../entities/order/model/orderTypes';
import { adminQueryKeys } from '../../../shared/api/queryKeys';
import { resultMessage } from '../../../shared/api/result';

export function useOrderManagementPage(pageSize: number) {
  const queryClient = useQueryClient();
  const [query, setQuery] = useState<AdminOrdersQuery>({ page: 1, pageSize });
  const [requestQuery, setRequestQuery] = useState<AdminOrdersQuery>({ page: 1, pageSize });
  const [detailLotId, setDetailLotId] = useState('');

  const ordersQuery = useQuery({
    queryKey: adminQueryKeys.orderList(requestQuery),
    queryFn: ({ signal }) => listAdminOrders(requestQuery, signal),
    placeholderData: keepPreviousData,
  });
  const detailQuery = useQuery({
    queryKey: adminQueryKeys.lotResult(detailLotId),
    queryFn: ({ signal }) => getLotResult(detailLotId, signal),
    enabled: Boolean(detailLotId),
  });

  const syncOrders = useCallback(async (nextQuery = query) => {
    const target = { ...nextQuery, page: nextQuery.page ?? 1, pageSize: nextQuery.pageSize ?? pageSize };
    setQuery(target);
    if (sameQuery(target, requestQuery)) {
      await queryClient.invalidateQueries({ queryKey: adminQueryKeys.orderList(requestQuery), exact: true });
      return;
    }
    setRequestQuery(target);
  }, [pageSize, query, queryClient, requestQuery]);

  const updateQuery = (patch: Partial<AdminOrdersQuery>) => {
    const next = { ...query, ...patch, page: patch.page ?? 1 };
    setQuery(next);
    if (patch.status !== undefined || patch.paymentStatus !== undefined || patch.page !== undefined) {
      setRequestQuery(next);
    }
  };

  const goPrevPage = () => {
    const next = { ...query, page: Math.max(1, (query.page || 1) - 1) };
    setQuery(next);
    setRequestQuery(next);
  };
  const goNextPage = () => {
    const next = { ...query, page: (query.page || 1) + 1 };
    setQuery(next);
    setRequestQuery(next);
  };
  const openDetail = (order: OrderSummary) => {
    setDetailLotId(order.lotId);
  };

  const page = ordersQuery.data;
  const orders = page?.orders ?? [];
  const total = page?.total ?? 0;
  const currentPage = page?.page ?? query.page ?? 1;
  const resolvedPageSize = page?.pageSize ?? query.pageSize ?? pageSize;
  const errorValue = (ordersQuery.error ? resultMessage(ordersQuery.error) : '') || (detailQuery.error ? resultMessage(detailQuery.error) : '');

  return {
    query,
    orders,
    total,
    loading: ordersQuery.isFetching,
    error: errorValue,
    detail: detailQuery.data ?? null,
    detailLoading: detailQuery.isFetching,
    closeDetail: () => setDetailLotId(''),
    totalPages: Math.max(1, Math.ceil(total / resolvedPageSize)),
    currentPage,
    goPrevPage,
    goNextPage,
    syncOrders,
    updateQuery,
    openDetail,
  };
}

function sameQuery(a: AdminOrdersQuery, b: AdminOrdersQuery) {
  return a.page === b.page
    && a.pageSize === b.pageSize
    && a.status === b.status
    && a.paymentStatus === b.paymentStatus
    && a.lotId === b.lotId
    && a.buyer === b.buyer;
}
