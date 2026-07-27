import type { RoleCode, User, UserStatus } from '../../../shared/api/types';
import { ROLE_CODE, USER_STATUS } from '../../../shared/api/types';
import type { StudioTone } from '../../../pages/host-console/components/studio-ui';

export const ROLE_CODE_FILTERS: Array<{ label: string; value: RoleCode | '' }> = [
  { label: '全部团队子账号', value: '' },
  { label: '主播 / 场控', value: ROLE_CODE.ANCHOR },
  { label: '运营子账号', value: ROLE_CODE.OPERATOR },
];

const ROLE_CODE_OPTIONS: Array<{ roleCode: RoleCode; label: string; hint: string; tone: StudioTone }> = [
  { roleCode: ROLE_CODE.MERCHANT_OWNER, label: '主账号', hint: '对应一个主播或商家主体，可管理自己的团队子账号', tone: 'purple' },
  { roleCode: ROLE_CODE.ANCHOR, label: '主播 / 场控', hint: '可进入工作台并执行开拍、控场、落锤等操作', tone: 'success' },
  { roleCode: ROLE_CODE.OPERATOR, label: '运营子账号', hint: '可进入后台协助拍品、队列和成交处理', tone: 'info' },
  { roleCode: ROLE_CODE.BUYER, label: '买家（H5）', hint: '只属于用户 H5，不允许进入或由后台创建', tone: 'warning' },
];

export const TEAM_ACCOUNT_ROLE_OPTIONS = [
  { roleCode: ROLE_CODE.ANCHOR, label: '主播 / 场控', hint: '协助直播中开拍、控场、落锤和异常处理', tone: 'success' as StudioTone },
  { roleCode: ROLE_CODE.OPERATOR, label: '运营子账号', hint: '协助拍品准备、队列维护和成交处理', tone: 'info' as StudioTone },
];

export function primaryRoleCode(roleCodes?: readonly string[] | null): string {
  if (!roleCodes?.length) return '';
  for (const roleCode of [ROLE_CODE.MERCHANT_OWNER, ROLE_CODE.ANCHOR, ROLE_CODE.OPERATOR, ROLE_CODE.BUYER]) {
    if (roleCodes.includes(roleCode)) return roleCode;
  }
  return roleCodes[0] || '';
}

export function isManagedTeamUser(user: Pick<User, 'roleCodes'>) {
  return user.roleCodes.some((roleCode) => roleCode === ROLE_CODE.ANCHOR || roleCode === ROLE_CODE.OPERATOR);
}

export function roleCodeMeta(roleCode?: RoleCode | string | null) {
  return ROLE_CODE_OPTIONS.find((item) => item.roleCode === roleCode) ?? {
    roleCode: String(roleCode || ''),
    label: String(roleCode || '未知角色'),
    hint: '后端返回的未识别角色',
    tone: 'neutral' as StudioTone,
  };
}

const USER_STATUS_OPTIONS: Array<{ status: UserStatus; label: string; hint: string; tone: StudioTone }> = [
  { status: USER_STATUS.ACTIVE, label: '启用中', hint: '可以登录后台并操作授权功能', tone: 'success' },
  { status: USER_STATUS.DISABLED, label: '已停用', hint: '不能登录，已有会话会被后端撤销', tone: 'danger' },
];

export const USER_STATUS_FILTERS: Array<{ label: string; value: UserStatus | '' }> = [
  { label: '全部状态', value: '' },
  { label: '启用中', value: USER_STATUS.ACTIVE },
  { label: '已停用', value: USER_STATUS.DISABLED },
];

export function userStatusMeta(status?: UserStatus | string | null) {
  return USER_STATUS_OPTIONS.find((item) => item.status === status) ?? {
    status: String(status || USER_STATUS.UNSPECIFIED),
    label: String(status || '未知状态'),
    hint: '后端返回的未识别状态',
    tone: 'neutral' as StudioTone,
  };
}
