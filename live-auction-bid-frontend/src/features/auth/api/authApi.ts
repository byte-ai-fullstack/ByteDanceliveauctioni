import { apiRequest } from '../../../shared/api/httpClient';
import { normalizeAuthTokens, normalizeUser } from '../../../shared/api/normalizers';
import { assertOkResult } from '../../../shared/api/result';
import { authSession } from '../../../shared/auth/authSession';
import { canAccessBackoffice } from '../../../shared/api/types';
import type { GetMeReply, LoginReply, LogoutReply, RegisterMerchantReply, ResetPasswordReply, User } from '../../../shared/api/types';

const BACKOFFICE_ACCESS_DENIED_MESSAGE = '该账号无后台访问权限';

export function currentAuth() {
  return authSession.current();
}

function setBackofficeSession(reply: LoginReply | RegisterMerchantReply) {
  if (!reply.user || !reply.tokens) throw new Error('auth response missing user or tokens');
  const user = normalizeUser(reply.user);
  const tokens = normalizeAuthTokens(reply.tokens);
  if (!canAccessBackoffice(user)) {
    authSession.clear();
    throw new Error(BACKOFFICE_ACCESS_DENIED_MESSAGE);
  }
  authSession.setAuthenticated(user, tokens);
  return user;
}

export async function login(username: string, password: string) {
  const reply = assertOkResult(await apiRequest<LoginReply>({
    path: '/api/users/login',
    method: 'POST',
    body: { username, password },
    auth: 'none',
    operation: 'login',
  }));
  return setBackofficeSession(reply);
}

export async function registerMerchant(username: string, password: string) {
  const reply = assertOkResult(await apiRequest<RegisterMerchantReply>({
    path: '/api/merchants/register',
    method: 'POST',
    body: { username, password },
    auth: 'none',
    operation: 'register-merchant',
  }));
  return setBackofficeSession(reply);
}

export async function resetPassword(username: string, password: string) {
  const reply = assertOkResult(await apiRequest<ResetPasswordReply>({
    path: '/api/users/reset-password',
    method: 'POST',
    body: { username, password },
    auth: 'none',
    operation: 'reset-password',
  }));
  return reply.user ? normalizeUser(reply.user) : null;
}

export async function me(): Promise<User | null> {
  if (!authSession.currentTokens()?.refreshToken) return null;
  const reply = assertOkResult(await apiRequest<GetMeReply>({
    path: '/api/users/me',
    method: 'GET',
    auth: 'required',
    operation: 'me',
  }));
  const user = reply.user ? normalizeUser(reply.user) : null;
  if (user && !canAccessBackoffice(user)) {
    authSession.clear();
    throw new Error(BACKOFFICE_ACCESS_DENIED_MESSAGE);
  }
  if (user) authSession.setAuthenticated(user, authSession.currentTokens()!);
  return user;
}

export async function logout() {
  const refreshToken = authSession.currentTokens()?.refreshToken;
  if (refreshToken) {
    await apiRequest<LogoutReply>({
      path: '/api/users/logout',
      method: 'POST',
      body: { refreshToken },
      auth: 'optional',
      operation: 'logout',
    }).catch(() => undefined);
  }
  authSession.clear();
}
