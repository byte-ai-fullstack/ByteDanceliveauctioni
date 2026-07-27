import { API_BASE } from '../config/env';
import { createRequestId } from '../lib/clientLogger';
import { authSession } from '../auth/authSession';
import { normalizeAuthTokens } from './normalizers';
import { publicResultMessage } from './result';
import type { AuthTokens, RefreshTokenReply, ReplyResult } from './types';
import { RESULT_CODE_LOGIN_REQUIRED, RESULT_CODE_OK, RESULT_CODE_SESSION_EXPIRED, RESULT_CODE_TOKEN_EXPIRED, RESULT_CODE_TOKEN_INVALID } from './types';

type AuthMode = 'none' | 'optional' | 'required';
type BodyMode = 'json' | 'form-data';

export type ApiRequestOptions = {
  method?: string;
  path: string;
  body?: unknown;
  auth?: AuthMode;
  bodyMode?: BodyMode;
  keepalive?: boolean;
  operation?: string;
  requestId?: string;
  headers?: HeadersInit;
  retryAuth?: boolean;
  signal?: AbortSignal;
};

type ParsedResponse<T> =
  | { ok: true; data: T; result?: Partial<ReplyResult>; requestId: string; status: number }
  | { ok: false; error: Error; result?: Partial<ReplyResult>; requestId: string; status: number; authExpired: boolean; authRefreshable: boolean };

class HttpError extends Error {
  readonly status: number;
  readonly requestId: string;

  constructor(status: number, message: string, requestId: string) {
    super(message);
    this.name = 'HttpError';
    this.status = status;
    this.requestId = requestId;
  }
}

class ApiResultError extends Error {
  readonly result: Partial<ReplyResult>;
  readonly requestId: string;

  constructor(result: Partial<ReplyResult>, requestId: string) {
    super(publicResultMessage(result));
    this.name = 'ApiResultError';
    this.result = result;
    this.requestId = requestId;
  }
}

class AuthExpiredError extends Error {
  readonly requestId: string;

  constructor(message: string, requestId: string) {
    super(message || '登录已过期，请重新登录');
    this.name = 'AuthExpiredError';
    this.requestId = requestId;
  }
}

function isAuthResultCode(code?: number) {
  return code === RESULT_CODE_LOGIN_REQUIRED || code === RESULT_CODE_TOKEN_EXPIRED || code === RESULT_CODE_TOKEN_INVALID || code === RESULT_CODE_SESSION_EXPIRED;
}

function isRefreshableAuthCode(code?: number) {
  return code === RESULT_CODE_TOKEN_EXPIRED;
}

function headerEntries(headers?: HeadersInit) {
  return new Headers(headers);
}

function buildURL(path: string) {
  if (/^https?:\/\//.test(path)) return path;
  return `${API_BASE}${path}`;
}

async function parseBody<T>(response: Response): Promise<T | null> {
  const text = await response.text();
  if (!text) return null;
  try {
    return JSON.parse(text) as T;
  } catch {
    return text as T;
  }
}

function responseRequestId(response: Response, fallback: string) {
  return response.headers.get('X-Request-Id') || response.headers.get('X-Trace-Id') || fallback;
}

function formatResponseError(input: { status: number; body: unknown; result?: Partial<ReplyResult>; requestId: string }) {
  const body = input.body as { message?: string; error?: string; code?: number; requestId?: string } | string | null;
  if (typeof body === 'string') return body || `HTTP ${input.status}`;
  return input.result ? publicResultMessage(input.result, body?.message || body?.error || `HTTP ${input.status}`) : body?.message || body?.error || `HTTP ${input.status}`;
}

async function sendOnce<T>(options: ApiRequestOptions, token: string | null, requestId: string): Promise<ParsedResponse<T>> {
  const bodyMode = options.bodyMode ?? (options.body instanceof FormData ? 'form-data' : 'json');
  const headers = headerEntries(options.headers);
  headers.set('X-Request-Id', requestId);
  headers.set('X-Client-App', 'admin-web');
  headers.set('X-Client-Version', import.meta.env.VITE_APP_VERSION || 'dev');
  headers.set('X-Client-Time', String(Date.now()));
  if (token) headers.set('Authorization', `Bearer ${token}`);
  let body: BodyInit | undefined;
  if (options.body !== undefined && options.body !== null) {
    if (bodyMode === 'form-data') {
      body = options.body as FormData;
    } else {
      headers.set('Content-Type', 'application/json');
      body = typeof options.body === 'string' ? options.body : JSON.stringify(options.body);
    }
  }
  const response = await fetch(buildURL(options.path), {
    method: options.method ?? (options.body ? 'POST' : 'GET'),
    headers,
    body,
    keepalive: options.keepalive,
    signal: options.signal,
  });
  const bodyData = await parseBody<T & { result?: Partial<ReplyResult> }>(response);
  const result = typeof bodyData === 'object' && bodyData ? bodyData.result : undefined;
  const responseId = responseRequestId(response, requestId);

  if (!response.ok) {
    const code = result?.code;
    const authExpired = isAuthResultCode(code) || response.status === 401;
    const authRefreshable = isRefreshableAuthCode(code);
    return {
      ok: false,
      error: authExpired
        ? new AuthExpiredError(formatResponseError({ status: response.status, body: bodyData, result, requestId: responseId }), responseId)
        : new HttpError(response.status, formatResponseError({ status: response.status, body: bodyData, result, requestId: responseId }), responseId),
      result,
      requestId: responseId,
      status: response.status,
      authExpired,
      authRefreshable,
    };
  }

  const code = result?.code ?? RESULT_CODE_OK;
  if (code !== RESULT_CODE_OK) {
    return {
      ok: false,
      error: isAuthResultCode(code) ? new AuthExpiredError(result?.message || '登录已过期，请重新登录', responseId) : new ApiResultError(result ?? { code }, responseId),
      result,
      requestId: responseId,
      status: response.status,
      authExpired: isAuthResultCode(code),
      authRefreshable: isRefreshableAuthCode(code),
    };
  }

  return { ok: true, data: bodyData as T, result, requestId: responseId, status: response.status };
}

export async function apiRequest<T>(options: ApiRequestOptions): Promise<T> {
  const auth = options.auth ?? 'required';
  const retryAuth = options.retryAuth ?? auth !== 'none';
  const requestId = options.requestId ?? createRequestId(options.operation ?? options.path.replace(/[^a-z0-9]+/gi, '-'));
  const token = auth === 'none' ? null : await authSession.getValidAccessToken();
  let parsed = await sendOnce<T>(options, token, requestId);
  if (!parsed.ok && parsed.authExpired && parsed.authRefreshable && retryAuth) {
    const refreshed = await authSession.refreshOnce();
    if (refreshed?.accessToken) {
      parsed = await sendOnce<T>(options, refreshed.accessToken, requestId);
    }
  }
  if (!parsed.ok) {
    if (parsed.authExpired) authSession.expire(parsed.error.message);
    throw parsed.error;
  }
  return parsed.data;
}

authSession.configureRefresh(async (refreshToken: string): Promise<AuthTokens> => {
  const reply = await apiRequest<RefreshTokenReply>({
    path: '/api/users/refresh',
    method: 'POST',
    body: { refreshToken },
    auth: 'none',
    retryAuth: false,
    operation: 'refresh-token',
  });
  if (!reply.tokens) throw new Error('refresh response missing tokens');
  return normalizeAuthTokens(reply.tokens);
});
