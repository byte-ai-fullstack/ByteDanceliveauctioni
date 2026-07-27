import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useState } from 'react';
import { listAdminUsers, type AdminUsersQuery } from '../../admin-user/api/adminUserApi';
import { isManagedTeamUser } from '../../../entities/user/model/userRole';
import { adminQueryKeys } from '../../../shared/api/queryKeys';
import { resultMessage } from '../../../shared/api/result';

export function useTeamAccountsPage(canListTeam: boolean, pageSize: number) {
  const queryClient = useQueryClient();
  const [query, setQuery] = useState<AdminUsersQuery>({ page: 1, pageSize });

  const fetchUsers = useCallback(async (signal?: AbortSignal) => {
    const page = await listAdminUsers(query, signal);
    const outOfScopeUser = page.users.find((user) => !isManagedTeamUser(user));
    if (outOfScopeUser) throw new Error(`团队账号接口返回了非子账号 ${outOfScopeUser.username}，违反主账号空间边界`);
    return page;
  }, [query]);

  const usersQuery = useQuery({
    queryKey: adminQueryKeys.userList(query),
    queryFn: ({ signal }) => fetchUsers(signal),
    enabled: canListTeam,
    placeholderData: keepPreviousData,
  });

  const syncUsers = useCallback(async (nextQuery = query) => {
    if (!canListTeam) return;
    if (!sameQuery(nextQuery, query)) {
      setQuery(nextQuery);
      await queryClient.fetchQuery({
        queryKey: adminQueryKeys.userList(nextQuery),
        queryFn: async ({ signal }) => {
          const page = await listAdminUsers(nextQuery, signal);
          const outOfScopeUser = page.users.find((user) => !isManagedTeamUser(user));
          if (outOfScopeUser) throw new Error(`团队账号接口返回了非子账号 ${outOfScopeUser.username}，违反主账号空间边界`);
          return page;
        },
      });
      return;
    }
    await queryClient.invalidateQueries({ queryKey: adminQueryKeys.userList(query), exact: true });
  }, [canListTeam, query, queryClient]);

  const runQuery = (nextQuery: AdminUsersQuery) => {
    setQuery(nextQuery);
  };

  const page = usersQuery.data;
  const total = page?.total ?? 0;
  const currentPage = page?.page ?? query.page ?? 1;
  const resolvedPageSize = page?.pageSize ?? query.pageSize ?? pageSize;

  return {
    query,
    users: page?.users ?? [],
    total,
    loading: usersQuery.isFetching,
    error: usersQuery.error ? resultMessage(usersQuery.error) : '',
    totalPages: Math.max(1, Math.ceil(total / resolvedPageSize)),
    currentPage,
    syncUsers,
    runQuery,
  };
}

function sameQuery(a: AdminUsersQuery, b: AdminUsersQuery) {
  return a.page === b.page
    && a.pageSize === b.pageSize
    && a.roleCode === b.roleCode
    && a.status === b.status
    && a.keyword === b.keyword;
}
