import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { me } from '../../features/auth/api/authApi';
import { resultMessage } from '../api/result';
import { hasPermission, type PermissionCode } from '../api/types';
import { authSession } from './authSession';
import type { AuthState } from './authStorage';
import { navigateApp } from '../router/historyStore';

type AuthSessionValue = {
  session: AuthState;
  refresh: () => Promise<unknown>;
};

const AuthSessionContext = createContext<AuthSessionValue | null>(null);

function currentPath() {
  return `${location.pathname}${location.search}${location.hash}`;
}

function isBackofficePath(pathname = location.pathname) {
  return pathname.startsWith('/host') || pathname.startsWith('/admin');
}

function loginURL(expired = false) {
  const next = currentPath().startsWith('/login') ? '/host' : currentPath();
  const params = new URLSearchParams({ next });
  if (expired) params.set('expired', '1');
  return `/login?${params.toString()}`;
}

function redirectToLogin(expired = false) {
  if (location.pathname.startsWith('/login')) return;
  navigateApp(loginURL(expired), true);
}

export function AuthSessionProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<AuthState>(() => authSession.current());

  useEffect(() => {
    const sync = () => setSession(authSession.current());
    const onExpired = () => {
      if (isBackofficePath() || location.pathname.startsWith('/login')) redirectToLogin(true);
    };
    window.addEventListener('auth-state-change', sync);
    window.addEventListener('storage', sync);
    window.addEventListener('auth:expired', onExpired);
    return () => {
      window.removeEventListener('auth-state-change', sync);
      window.removeEventListener('storage', sync);
      window.removeEventListener('auth:expired', onExpired);
    };
  }, []);

  useEffect(() => {
    const refreshIfNeeded = () => {
      if (authSession.isAccessTokenExpiringSoon()) void authSession.refreshOnce();
    };
    const onVisibility = () => {
      if (document.visibilityState === 'visible') refreshIfNeeded();
    };
    const interval = window.setInterval(refreshIfNeeded, 30_000);
    window.addEventListener('focus', refreshIfNeeded);
    document.addEventListener('visibilitychange', onVisibility);
    refreshIfNeeded();
    return () => {
      window.clearInterval(interval);
      window.removeEventListener('focus', refreshIfNeeded);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, []);

  const value = useMemo<AuthSessionValue>(() => ({
    session,
    refresh: () => authSession.refreshOnce(),
  }), [session]);

  return <AuthSessionContext.Provider value={value}>{children}</AuthSessionContext.Provider>;
}

function useAuthSession() {
  const value = useContext(AuthSessionContext);
  if (!value) throw new Error('useAuthSession must be used inside AuthSessionProvider');
  return value;
}

export function ProtectedRoute({ children, requiredPermissions }: { children: ReactNode; requiredPermissions?: PermissionCode[] }) {
  const { session } = useAuthSession();
  const [checking, setChecking] = useState(true);
  const [error, setError] = useState('');
  const [denied, setDenied] = useState(false);
  const requiredPermissionsKey = requiredPermissions?.join('|') ?? '';
  const userPermissionsKey = session.user?.permissionCodes?.join('|') ?? '';

  useEffect(() => {
    let cancelled = false;
    async function checkSession() {
      setChecking(true);
      setError('');
      setDenied(false);
      const tokens = authSession.currentTokens();
      if (!tokens?.refreshToken) {
        redirectToLogin(false);
        return;
      }
      try {
        const token = await authSession.getValidAccessToken();
        if (!token) {
          redirectToLogin(true);
          return;
        }
        const user = await me();
        if (cancelled) return;
        if (!user) {
          redirectToLogin(true);
          return;
        }
        if (requiredPermissions?.length && !requiredPermissions.some((permissionCode) => hasPermission(user, permissionCode))) {
          authSession.clear();
          setDenied(true);
          return;
        }
      } catch (e) {
        if (!cancelled) setError(resultMessage(e));
      } finally {
        if (!cancelled) setChecking(false);
      }
    }
    void checkSession();
    return () => {
      cancelled = true;
    };
  }, [session.tokens?.accessToken, session.tokens?.refreshToken, userPermissionsKey, requiredPermissionsKey, requiredPermissions]);

  if (checking) return <main className="routeLoading" aria-busy="true"><span>LiveAuction Studio</span><b>正在验证登录态...</b></main>;
  if (denied) return <main className="routeLoading"><span>LiveAuction Studio</span><b>当前账号无权访问管理后台</b></main>;
  if (error) return <main className="routeLoading"><span>LiveAuction Studio</span><b>{error}</b></main>;
  return <>{children}</>;
}
