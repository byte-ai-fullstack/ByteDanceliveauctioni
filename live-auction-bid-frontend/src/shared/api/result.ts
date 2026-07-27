import type { ReplyResult } from './types';
import {
  RESULT_CODE_BID_ALREADY_LEADING,
  RESULT_CODE_BID_CURRENCY_MISMATCH,
  RESULT_CODE_BID_ENDED,
  RESULT_CODE_BID_NOT_LIVE,
  RESULT_CODE_BID_TOO_LOW,
  RESULT_CODE_BID_VERSION_STALE,
  RESULT_CODE_INTERNAL_ERROR,
  RESULT_CODE_INVALID_CREDENTIALS,
  RESULT_CODE_LOGIN_REQUIRED,
  RESULT_CODE_LOT_CANCELLED,
  RESULT_CODE_LOT_VERSION_CONFLICT,
  RESULT_CODE_OK,
  RESULT_CODE_PROJECTION_PENDING,
  RESULT_CODE_QUEUE_POSITION_CONFLICT,
  RESULT_CODE_ROOM_ACTIVE_LOT_EXISTS,
  RESULT_CODE_USERNAME_TAKEN,
  RESULT_CODE_FORBIDDEN,
  RESULT_CODE_ACCOUNT_DISABLED,
  RESULT_CODE_INVALID_ARGUMENT,
  RESULT_CODE_SESSION_EXPIRED,
  RESULT_CODE_TOKEN_EXPIRED,
  RESULT_CODE_TOKEN_INVALID,
  RESULT_CODE_USER_NOT_FOUND,
} from './types';

class ApiResultError extends Error {
  readonly result: ReplyResult;

  constructor(result: ReplyResult) {
    super(publicResultMessage(result));
    this.name = 'ApiResultError';
    this.result = result;
  }
}

export function assertOkResult<T extends { result?: Partial<ReplyResult> }>(reply: T): T {
  const result = reply.result;
  // proto3 JSON 会省略默认值 code=0，所以 { message: 'ok' } 也应视为成功。
  if (result && (result.code ?? RESULT_CODE_OK) !== RESULT_CODE_OK) {
    throw new ApiResultError(result as ReplyResult);
  }
  return reply;
}

export function resultMessage(e: unknown): string {
  const result = (e as { result?: Partial<ReplyResult> } | null)?.result;
  if (result) return publicResultMessage(result);
  if (e instanceof ApiResultError) return publicResultMessage(e.result);
  if (e instanceof Error) return e.message;
  if (typeof Event !== 'undefined' && e instanceof Event) return '实时连接异常，正在尝试重连';
  const message = String(e);
  return message === '[object Event]' ? '实时连接异常，正在尝试重连' : message;
}

const errorMessages: Record<number, string> = {
  [RESULT_CODE_INVALID_ARGUMENT]: '参数不正确，请检查后重试',
  [RESULT_CODE_LOGIN_REQUIRED]: '请先登录后再操作',
  [RESULT_CODE_TOKEN_EXPIRED]: '登录已过期，正在刷新登录态',
  [RESULT_CODE_TOKEN_INVALID]: '登录凭证无效，请重新登录',
  [RESULT_CODE_SESSION_EXPIRED]: '登录会话已失效，请重新登录',
  [RESULT_CODE_INVALID_CREDENTIALS]: '用户名或密码不正确',
  [RESULT_CODE_FORBIDDEN]: '当前账号没有执行该操作的权限',
  [RESULT_CODE_ACCOUNT_DISABLED]: '当前账号已停用，请联系管理员',
  [RESULT_CODE_USER_NOT_FOUND]: '用户或资源不存在',
  [RESULT_CODE_LOT_VERSION_CONFLICT]: '竞拍状态已变化，已刷新最新数据后再操作',
  [RESULT_CODE_USERNAME_TAKEN]: '用户名已存在，请直接登录或换一个用户名',
  [RESULT_CODE_ROOM_ACTIVE_LOT_EXISTS]: '当前直播间已有正在竞拍的拍品，请先成交或取消当前拍品',
  [RESULT_CODE_QUEUE_POSITION_CONFLICT]: '当前直播间队列正在更新，请刷新后重试',
  [RESULT_CODE_BID_TOO_LOW]: '出价金额太低，请按建议价重新出价',
  [RESULT_CODE_BID_NOT_LIVE]: '本件拍品还未开始，请先确认当前讲解商品',
  [RESULT_CODE_BID_ENDED]: '竞拍已结束，请处理下一件拍品',
  [RESULT_CODE_BID_ALREADY_LEADING]: '当前买家已经领先，需等待其他人出价',
  [RESULT_CODE_BID_CURRENCY_MISMATCH]: '出价币种异常，请刷新后重试',
  [RESULT_CODE_BID_VERSION_STALE]: '竞拍价格已更新，请刷新后重试',
  [RESULT_CODE_LOT_CANCELLED]: '本件拍品已取消，无法继续出价',
  [RESULT_CODE_PROJECTION_PENDING]: '出价数据正在同步，请稍后刷新',
  [RESULT_CODE_INTERNAL_ERROR]: '系统暂时不可用，请稍后重试',
};

const businessMessages: Record<string, string> = {
  INVALID_ARGUMENT: '参数不正确，请检查后重试',
  'invalid argument': '参数不正确，请检查后重试',
  'login required': '请先登录后再操作',
  'permission denied': '当前账号没有执行该操作的权限',
  'account disabled': '当前账号已停用，请联系管理员',
  'username already exists': '用户名已存在，请直接登录或换一个用户名',
  'invalid username or password': '用户名或密码不正确',
  'user not found': '用户或资源不存在',
  'not found': '用户或资源不存在',
  'internal error, please try again later': '系统暂时不可用，请稍后重试',
  BID_REJECTED: '出价失败，请调整金额后重试',
  BID_TOO_LOW: '出价金额太低，请按建议价重新出价',
  BID_NOT_LIVE: '本件拍品还未开始，请先确认当前讲解商品',
  BID_ENDED: '竞拍已结束，请处理下一件拍品',
  BID_ALREADY_LEADING: '当前买家已经领先，需等待其他人出价',
  BID_CURRENCY_MISMATCH: '出价币种异常，请刷新后重试',
  BID_VERSION_STALE: '竞拍价格已更新，请刷新后重试',
  LOT_CANCELLED: '本件拍品已取消，无法继续出价',
  ROOM_ACTIVE_LOT_EXISTS: '当前直播间已有正在竞拍的拍品，请先成交或取消当前拍品',
  PROJECTION_PENDING: '出价数据正在同步，请稍后刷新',
};

export function publicResultMessage(result?: Partial<ReplyResult>, fallback = '请求失败') {
  if (!result) return fallback;
  const code = Number(result.code ?? RESULT_CODE_OK);
  const rawMessage = String(result.message || '').trim();
  if (code === RESULT_CODE_INVALID_ARGUMENT && /no accepted bid|无有效出价|暂无有效出价|winner is required/i.test(rawMessage)) {
    return '暂无有效出价，不能落锤成交。请等待买家出价，或使用异常取消处理本件拍品。';
  }
  return errorMessages[code] || businessMessages[rawMessage] || fallback;
}
