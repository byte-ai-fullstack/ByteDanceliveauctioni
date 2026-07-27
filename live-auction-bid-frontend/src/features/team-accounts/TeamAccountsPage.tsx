import { useMemo, useState, type FormEvent } from 'react';
import { AlertTriangle, Ban, CheckCircle2, ChevronLeft, ChevronRight, KeyRound, Pencil, Plus, Power, RefreshCw, Search, ShieldAlert, ShieldCheck, UserCog, Users } from 'lucide-react';
import { currentAuth } from '../auth/api/authApi';
import { adminCreateUser, adminResetUserPassword, adminUpdateUserRole, adminUpdateUserStatus, type AdminUsersQuery } from '../admin-user/api/adminUserApi';
import { hasPermission, PERMISSION_CODE, ROLE_CODE, USER_STATUS, type RoleCode, type User, type UserStatus } from '../../shared/api/types';
import { resultMessage } from '../../shared/api/result';
import { formatDateTimeText } from '../../shared/lib/format';
import { isManagedTeamUser, primaryRoleCode, ROLE_CODE_FILTERS, roleCodeMeta, TEAM_ACCOUNT_ROLE_OPTIONS, USER_STATUS_FILTERS, userStatusMeta } from '../../entities/user/model/userRole';
import { StudioBadge, StudioButton, StudioCard, StudioDialog, StudioDrawer, StudioEmptyState, StudioField, StudioMetricCard, StudioPageHeader, StudioTable, StudioTableSkeleton, StudioToastViewport } from '../../pages/host-console/components/studio-ui';
import { useStudioToast } from '../../pages/host-console/components/studio-toast';
import { useTeamAccountsPage } from './model/useTeamAccountsPage';

const DEFAULT_PAGE_SIZE = 20;
const MANAGED_TEAM_ROLES: RoleCode[] = [ROLE_CODE.ANCHOR, ROLE_CODE.OPERATOR];
const EMPTY_CREATE_FORM = { username: '', password: '', nickname: '', roleCode: ROLE_CODE.OPERATOR as RoleCode };

export function TeamAccountsPage() {
  const currentUser = currentAuth().user;
  const canListTeam = hasPermission(currentUser, PERMISSION_CODE.TEAM_USER_LIST);
  const canCreateTeam = hasPermission(currentUser, PERMISSION_CODE.TEAM_USER_CREATE);
  const canUpdateTeamRole = hasPermission(currentUser, PERMISSION_CODE.TEAM_USER_UPDATE_ROLE);
  const canUpdateTeamStatus = hasPermission(currentUser, PERMISSION_CODE.TEAM_USER_UPDATE_STATUS);
  const canResetTeamPassword = hasPermission(currentUser, PERMISSION_CODE.TEAM_USER_RESET_PASSWORD);
  const [keywordDraft, setKeywordDraft] = useState('');
  const [actionError, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState(EMPTY_CREATE_FORM);
  const [creating, setCreating] = useState(false);
  const [roleTarget, setRoleTarget] = useState<User | null>(null);
  const [roleCode, setRoleCode] = useState<RoleCode>(ROLE_CODE.OPERATOR);
  const [updatingRole, setUpdatingRole] = useState(false);
  const [statusTarget, setStatusTarget] = useState<User | null>(null);
  const [updatingStatus, setUpdatingStatus] = useState(false);
  const [passwordTarget, setPasswordTarget] = useState<User | null>(null);
  const [resetPassword, setResetPassword] = useState('');
  const [resettingPassword, setResettingPassword] = useState(false);
  const { toasts, showToast } = useStudioToast();
  const { query, users, total, loading, error: queryError, totalPages, currentPage, syncUsers: syncUsersQuery, runQuery } = useTeamAccountsPage(canListTeam, DEFAULT_PAGE_SIZE);
  const error = actionError || queryError;
  const syncUsers = async (nextQuery = query) => {
    setError('');
    try {
      await syncUsersQuery(nextQuery);
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ id: 'team-users-sync-failed', tone: 'danger', title: '账号列表同步失败', description: message });
    }
  };
  const canSubmitCreate = canCreateTeam && createForm.username.trim().length > 0 && createForm.nickname.trim().length > 0 && createForm.password.length >= 8 && MANAGED_TEAM_ROLES.includes(createForm.roleCode);
  const canSubmitResetPassword = canResetTeamPassword && resetPassword.length >= 8 && resetPassword.length <= 128;

  const submitSearch = () => {
    runQuery({ ...query, keyword: keywordDraft.trim(), page: 1 });
  };

  const goPrevPage = () => runQuery({ ...query, page: Math.max(1, currentPage - 1) });
  const goNextPage = () => runQuery({ ...query, page: Math.min(totalPages, currentPage + 1) });

  const submitCreate = async (event?: FormEvent) => {
    event?.preventDefault();
    if (!canSubmitCreate) return;
    setCreating(true);
    setError('');
    try {
      const user = await adminCreateUser({ ...createForm, username: createForm.username.trim(), nickname: createForm.nickname.trim() });
      setCreateForm(EMPTY_CREATE_FORM);
      setCreateOpen(false);
      showToast({ tone: 'success', title: '账号已创建', description: `${user.username} · ${roleCodeMeta(primaryRoleCode(user.roleCodes)).label}` });
      await syncUsers({ ...query, page: 1 });
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '创建账号失败', description: message });
    } finally {
      setCreating(false);
    }
  };

  const openRoleDialog = (user: User) => {
    setRoleTarget(user);
    setRoleCode(teamRoleCode(user));
  };

  const submitRoleUpdate = async () => {
    if (!canUpdateTeamRole || !roleTarget || !MANAGED_TEAM_ROLES.includes(roleCode)) return;
    setUpdatingRole(true);
    setError('');
    try {
      const user = await adminUpdateUserRole(roleTarget.id, roleCode);
      setRoleTarget(null);
      showToast({ tone: 'success', title: '账号角色已更新', description: `${user.username} · ${roleCodeMeta(primaryRoleCode(user.roleCodes)).label}` });
      await syncUsers(query);
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '更新角色失败', description: message });
    } finally {
      setUpdatingRole(false);
    }
  };

  const submitStatusUpdate = async () => {
    if (!canUpdateTeamStatus || !statusTarget || !isManagedTeamUser(statusTarget)) return;
    const nextStatus = nextUserStatus(statusTarget);
    setUpdatingStatus(true);
    setError('');
    try {
      const updated = await adminUpdateUserStatus(statusTarget.id, nextStatus);
      setStatusTarget(null);
      showToast({ tone: 'success', title: nextStatus === USER_STATUS.ACTIVE ? '账号已启用' : '账号已停用', description: `${updated.username} · ${userStatusMeta(updated.status).label}` });
      await syncUsers(query);
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '账号状态更新失败', description: message });
    } finally {
      setUpdatingStatus(false);
    }
  };

  const submitPasswordReset = async (event?: FormEvent) => {
    event?.preventDefault();
    if (!passwordTarget || !canSubmitResetPassword) return;
    setResettingPassword(true);
    setError('');
    try {
      const updated = await adminResetUserPassword(passwordTarget.id, resetPassword);
      setPasswordTarget(null);
      setResetPassword('');
      showToast({ tone: 'success', title: '密码已重置', description: `${updated.username} 的旧会话已撤销` });
      await syncUsers(query);
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '重置密码失败', description: message });
    } finally {
      setResettingPassword(false);
    }
  };

  const metrics = useMemo(() => ({
    subaccounts: users.filter(isManagedTeamUser).length,
    active: users.filter((user) => user.status === USER_STATUS.ACTIVE).length,
    disabled: users.filter((user) => user.status === USER_STATUS.DISABLED).length,
  }), [users]);

  const statusNext = statusTarget ? nextUserStatus(statusTarget) : USER_STATUS.ACTIVE;
  const roleTargetMeta = roleTarget ? roleCodeMeta(primaryRoleCode(roleTarget.roleCodes)) : null;

  return <section className="teamAccountPage">
    <StudioToastViewport toasts={toasts} />
    <section className="laSettingsHero laMerchantsHero">
      <StudioPageHeader
        eyebrow="Team accounts"
        title="团队成员"
        description="每个主账号只管理自己主播 / 商家空间下的主播、场控和运营子账号。"
        actions={<div className="teamAccountHeroActions">
          <StudioButton type="button" variant="secondary" icon={<RefreshCw size={15} />} loading={loading} disabled={!canListTeam} onClick={() => void syncUsers({ ...query, keyword: keywordDraft.trim() })}>{loading ? '同步中' : '同步账号'}</StudioButton>
          <StudioButton type="button" variant="primary" icon={<Plus size={15} />} disabled={!canCreateTeam} onClick={() => setCreateOpen(true)}>新建成员</StudioButton>
        </div>}
      />
    </section>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    {!canListTeam ? <NoTeamPermissionNotice currentUser={currentUser} /> : <>
      <section className="laStatsGrid">
        <StudioMetricCard icon={<ShieldCheck />} label="筛选结果" value={total} trend="后端返回 total" tone="info" />
        <StudioMetricCard icon={<UserCog />} label="本页子账号" value={metrics.subaccounts} trend="主播 / 场控 / 运营" tone="purple" />
        <StudioMetricCard icon={<Power />} label="本页启用中" value={metrics.active} trend="允许登录后台" tone="success" />
        <StudioMetricCard icon={<Ban />} label="本页已停用" value={metrics.disabled} trend="禁止登录并撤销会话" tone="danger" />
      </section>

      <StudioCard padding="md" className="teamAccountFilterCard">
        <div className="teamAccountFilters" aria-label="账号筛选">
          <label className="teamAccountSearchField"><Search size={15} /><input value={keywordDraft} onChange={(e) => setKeywordDraft(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') submitSearch(); }} placeholder="搜索用户 ID / 用户名 / 昵称" /></label>
          <StudioField label="角色"><select value={query.roleCode || ''} onChange={(e) => runQuery({ ...query, roleCode: e.target.value as AdminUsersQuery['roleCode'], page: 1 })}>{ROLE_CODE_FILTERS.map((item) => <option key={item.label} value={item.value}>{item.label}</option>)}</select></StudioField>
          <StudioField label="状态"><select value={query.status || ''} onChange={(e) => runQuery({ ...query, status: e.target.value as AdminUsersQuery['status'], page: 1 })}>{USER_STATUS_FILTERS.map((item) => <option key={item.label} value={item.value}>{item.label}</option>)}</select></StudioField>
          <StudioButton type="button" variant="primary" icon={<Search size={15} />} loading={loading} onClick={submitSearch}>查询</StudioButton>
        </div>
      </StudioCard>

      {loading ? <StudioTableSkeleton rows={6} columns={6} /> : <StudioTable
        className="teamAccountTable"
        rows={users}
        rowKey={(user) => user.id}
        header={`共 ${total} 个账号 · 每页 ${query.pageSize || DEFAULT_PAGE_SIZE} 条`}
        filters={<div className="orderPager">
          <button type="button" disabled={currentPage <= 1 || loading} onClick={goPrevPage}><ChevronLeft size={15} /><span>上一页</span></button>
          <span className="orderPagerIndex">第 {currentPage} / {totalPages} 页</span>
          <button type="button" disabled={currentPage >= totalPages || loading} onClick={goNextPage}><span>下一页</span><ChevronRight size={15} /></button>
        </div>}
        empty={<StudioEmptyState compact icon={<Users size={28} />} title="暂无账号" description="当前筛选条件下没有团队成员。" action={canCreateTeam ? <StudioButton type="button" variant="primary" icon={<Plus size={15} />} onClick={() => setCreateOpen(true)}>新建成员</StudioButton> : null} />}
        columns={[
          { label: '用户', render: (user) => <div className="teamAccountUserCell"><span className="teamAccountAvatar">{userInitial(user)}</span><div><b>{user.username}</b><span>{user.nickname || '未设置昵称'}</span><code>{user.id}</code></div></div> },
          { label: '角色', render: (user) => <TeamRoleBadge user={user} /> },
          { label: '状态', render: (user) => <StudioBadge tone={userStatusMeta(user.status).tone}>{userStatusMeta(user.status).label}</StudioBadge> },
          { label: '创建时间', render: (user) => formatDateTimeText(user.createdAtUnixMs) },
          { label: '更新时间', render: (user) => formatDateTimeText(user.updatedAtUnixMs) },
          { label: '操作', render: (user) => <div className="teamAccountActions">
            <StudioButton type="button" size="sm" variant="secondary" icon={<Pencil size={14} />} disabled={!canUpdateTeamRole} onClick={() => openRoleDialog(user)}>角色</StudioButton>
            <StudioButton type="button" size="sm" variant={user.status === USER_STATUS.ACTIVE ? 'danger' : 'secondary'} icon={user.status === USER_STATUS.ACTIVE ? <Ban size={14} /> : <Power size={14} />} disabled={!canUpdateTeamStatus} onClick={() => setStatusTarget(user)}>{user.status === USER_STATUS.ACTIVE ? '停用' : '启用'}</StudioButton>
            <StudioButton type="button" size="sm" variant="soft" icon={<KeyRound size={14} />} disabled={!canResetTeamPassword} onClick={() => { setPasswordTarget(user); setResetPassword(''); }}>密码</StudioButton>
          </div> },
        ]}
      />}
    </>}

    {createOpen ? <StudioDrawer
      eyebrow="/api/admin/users"
      title="新建团队成员"
      description="只能创建主播 / 场控或运营子账号。"
      onClose={() => setCreateOpen(false)}
      footer={<>
        <StudioButton type="button" variant="secondary" onClick={() => setCreateOpen(false)}>取消</StudioButton>
        <StudioButton type="submit" form="teamAccountCreateForm" variant="primary" icon={<UserCog size={15} />} loading={creating} disabled={!canSubmitCreate}>创建账号</StudioButton>
      </>}
    >
      <form id="teamAccountCreateForm" className="teamAccountForm" onSubmit={(event) => void submitCreate(event)}>
        <StudioField label="用户名"><input value={createForm.username} onChange={(e) => setCreateForm({ ...createForm, username: e.target.value })} placeholder="team_operator_01" autoComplete="off" /></StudioField>
        <StudioField label="昵称"><input value={createForm.nickname} onChange={(e) => setCreateForm({ ...createForm, nickname: e.target.value })} placeholder="运营账号昵称" /></StudioField>
        <StudioField label="初始密码" help="8-128 个字符"><input value={createForm.password} onChange={(e) => setCreateForm({ ...createForm, password: e.target.value })} placeholder="输入后端允许的密码" type="password" minLength={8} maxLength={128} autoComplete="new-password" /></StudioField>
        <StudioField label="角色"><select value={createForm.roleCode} onChange={(e) => setCreateForm({ ...createForm, roleCode: e.target.value as RoleCode })}>{TEAM_ACCOUNT_ROLE_OPTIONS.map((item) => <option key={item.roleCode} value={item.roleCode}>{item.label}</option>)}</select></StudioField>
        <div className="teamAccountRoleChoices">{TEAM_ACCOUNT_ROLE_OPTIONS.map((item) => <button key={item.roleCode} className={createForm.roleCode === item.roleCode ? 'active' : ''} type="button" aria-pressed={createForm.roleCode === item.roleCode} onClick={() => setCreateForm({ ...createForm, roleCode: item.roleCode })}><StudioBadge tone={item.tone}>{item.label}</StudioBadge><span>{item.hint}</span></button>)}</div>
      </form>
    </StudioDrawer> : null}

    {roleTarget ? <StudioDialog
      eyebrow="/api/admin/users/{userId}/role"
      title="调整角色"
      description={roleTarget.username}
      onClose={() => setRoleTarget(null)}
      footer={<>
        <StudioButton type="button" variant="secondary" onClick={() => setRoleTarget(null)}>取消</StudioButton>
        <StudioButton type="button" variant="primary" icon={<CheckCircle2 size={15} />} loading={updatingRole} disabled={!canUpdateTeamRole} onClick={() => void submitRoleUpdate()}>更新角色</StudioButton>
      </>}
    >
      <div className="teamAccountDialogSummary">
        <span>{roleTarget.nickname || roleTarget.username}</span>
        {roleTargetMeta ? <StudioBadge tone={roleTargetMeta.tone}>{roleTargetMeta.label}</StudioBadge> : null}
      </div>
      <StudioField label="新角色"><select value={roleCode} onChange={(e) => setRoleCode(e.target.value as RoleCode)}>{TEAM_ACCOUNT_ROLE_OPTIONS.map((item) => <option key={item.roleCode} value={item.roleCode}>{item.label}</option>)}</select></StudioField>
    </StudioDialog> : null}

    {statusTarget ? <StudioDialog
      eyebrow="/api/admin/users/{userId}/status"
      title={statusNext === USER_STATUS.ACTIVE ? '启用账号' : '停用账号'}
      description={statusTarget.username}
      onClose={() => setStatusTarget(null)}
      className={statusNext === USER_STATUS.DISABLED ? 'studioDialogDanger' : ''}
      footer={<>
        <StudioButton type="button" variant="secondary" onClick={() => setStatusTarget(null)}>取消</StudioButton>
        <StudioButton type="button" variant={statusNext === USER_STATUS.DISABLED ? 'danger' : 'primary'} icon={statusNext === USER_STATUS.DISABLED ? <Ban size={15} /> : <Power size={15} />} loading={updatingStatus} disabled={!canUpdateTeamStatus} onClick={() => void submitStatusUpdate()}>{statusNext === USER_STATUS.ACTIVE ? '启用账号' : '停用账号'}</StudioButton>
      </>}
    >
      <StudioEmptyState compact tone={statusNext === USER_STATUS.ACTIVE ? 'success' : 'danger'} icon={statusNext === USER_STATUS.ACTIVE ? <Power size={24} /> : <Ban size={24} />} title={statusNext === USER_STATUS.ACTIVE ? '恢复后台登录' : '停止后台登录'} description={statusNext === USER_STATUS.ACTIVE ? '启用后该成员可按角色权限进入后台。' : '停用后该成员不能登录，后端会撤销已有会话。'} />
    </StudioDialog> : null}

    {passwordTarget ? <StudioDialog
      eyebrow="/api/admin/users/{userId}/reset-password"
      title="重置密码"
      description={passwordTarget.username}
      onClose={() => setPasswordTarget(null)}
      footer={<>
        <StudioButton type="button" variant="secondary" onClick={() => setPasswordTarget(null)}>取消</StudioButton>
        <StudioButton type="submit" form="teamAccountResetPasswordForm" variant="danger" icon={<KeyRound size={15} />} loading={resettingPassword} disabled={!canSubmitResetPassword}>重置密码</StudioButton>
      </>}
    >
      <form id="teamAccountResetPasswordForm" className="teamAccountForm" onSubmit={(event) => void submitPasswordReset(event)}>
        <div className="teamAccountDialogSummary"><span>{passwordTarget.nickname || passwordTarget.username}</span><StudioBadge tone={userStatusMeta(passwordTarget.status).tone}>{userStatusMeta(passwordTarget.status).label}</StudioBadge></div>
        <StudioField label="新密码" help="8-128 个字符"><input value={resetPassword} onChange={(e) => setResetPassword(e.target.value)} placeholder="输入新密码" type="password" minLength={8} maxLength={128} autoComplete="new-password" /></StudioField>
      </form>
    </StudioDialog> : null}
  </section>;
}

function TeamRoleBadge({ user }: { user: User }) {
  const meta = roleCodeMeta(primaryRoleCode(user.roleCodes));
  return <div className="teamAccountRoleCell"><StudioBadge tone={meta.tone}>{meta.label}</StudioBadge><span>{meta.hint}</span></div>;
}

function NoTeamPermissionNotice({ currentUser }: { currentUser?: User | null }) {
  return <>
    <section className="laStatsGrid">
      <StudioMetricCard icon={<ShieldCheck />} label="当前后台账号" value={currentUser?.username || '未同步'} trend={currentUser ? roleCodeMeta(primaryRoleCode(currentUser.roleCodes)).label : 'AuthSession'} tone="info" />
      <StudioMetricCard icon={<ShieldAlert />} label="账号管理权限" value="未授权" trend="缺少 team.user.list 权限" tone="danger" />
    </section>
    <StudioCard title="仅主账号可管理团队成员" subtitle="Main account only" padding="md">
      <StudioEmptyState compact icon={<ShieldAlert size={28} />} title="仅主账号可管理团队成员" description="当前账号只能查看后台业务功能，不能看到或提交创建团队子账号、修改角色、停用账号表单。" />
    </StudioCard>
  </>;
}

function teamRoleCode(user: User): RoleCode {
  const roleCode = primaryRoleCode(user.roleCodes);
  return MANAGED_TEAM_ROLES.includes(roleCode as RoleCode) ? roleCode as RoleCode : ROLE_CODE.OPERATOR;
}

function nextUserStatus(user: User): UserStatus {
  return user.status === USER_STATUS.ACTIVE ? USER_STATUS.DISABLED : USER_STATUS.ACTIVE;
}

function userInitial(user: User) {
  return (user.nickname || user.username || 'U').slice(0, 1).toUpperCase();
}
