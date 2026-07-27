import { useQuery } from '@tanstack/react-query';
import { listAdminRooms } from '../../../features/auction/api/auctionApi';
import { adminQueryKeys } from '../../../shared/api/queryKeys';
import { resultMessage } from '../../../shared/api/result';

export function useHostConsolePage() {
  const roomsQuery = useQuery({
    queryKey: adminQueryKeys.rooms(),
    queryFn: ({ signal }) => listAdminRooms({ signal }),
    staleTime: 60_000,
  });

  return {
    room: roomsQuery.data?.[0] ?? null,
    loading: roomsQuery.isPending,
    error: roomsQuery.error ? resultMessage(roomsQuery.error) : '',
  };
}
