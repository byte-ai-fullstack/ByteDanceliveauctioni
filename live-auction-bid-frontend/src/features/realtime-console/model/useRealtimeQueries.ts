import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useCallback } from 'react';
import { getRoomSnapshot, listAdminLots } from '../../auction/api/auctionApi';
import { adminQueryKeys } from '../../../shared/api/queryKeys';
import { resultMessage } from '../../../shared/api/result';

export function useLiveControlQuery(roomId: string) {
  const query = useQuery({
    queryKey: adminQueryKeys.liveControl(roomId),
    queryFn: async ({ signal }) => {
      const [snapshot, page] = await Promise.all([
        getRoomSnapshot(roomId, signal),
        listAdminLots({ page: 1, pageSize: 20, roomId, view: 'current' }, signal),
      ]);
      return { snapshot, lots: page.lots };
    },
    placeholderData: keepPreviousData,
  });
  const refetch = query.refetch;
  const sync = useCallback(async () => {
    const result = await refetch({ throwOnError: true });
    if (!result.data) throw new Error('房间状态同步失败');
    return result.data;
  }, [refetch]);

  return {
    snapshot: query.data?.snapshot ?? null,
    lots: query.data?.lots ?? [],
    loading: query.isFetching,
    error: query.error ? resultMessage(query.error) : '',
    sync,
  };
}

export function useRoomSnapshotQuery(roomId: string) {
  const query = useQuery({
    queryKey: adminQueryKeys.roomSnapshot(roomId),
    queryFn: ({ signal }) => getRoomSnapshot(roomId, signal),
    placeholderData: keepPreviousData,
  });
  const refetch = query.refetch;
  const sync = useCallback(async () => {
    const result = await refetch({ throwOnError: true });
    if (!result.data) throw new Error('房间快照同步失败');
    return result.data;
  }, [refetch]);

  return {
    snapshot: query.data ?? null,
    loading: query.isFetching,
    error: query.error ? resultMessage(query.error) : '',
    dataUpdatedAt: query.dataUpdatedAt,
    sync,
  };
}
