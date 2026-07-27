import { Suspense, lazy, type ReactNode } from 'react';
import { AuthSessionProvider, ProtectedRoute } from '../shared/auth/AuthSessionProvider';
import { BACKOFFICE_ACCESS_PERMISSIONS } from '../shared/api/types';
import { useAppLocation } from '../shared/router/historyStore';

const HomePage = lazy(() => import('../pages/home/HomePage').then((module) => ({ default: module.HomePage })));
const LoginPage = lazy(() => import('../pages/login/LoginPage').then((module) => ({ default: module.LoginPage })));
const HostConsolePage = lazy(() => import('../pages/host-console/HostConsolePage').then((module) => ({ default: module.HostConsolePage })));

function isBackofficePath(pathname: string) {
  return pathname.startsWith('/host') || pathname.startsWith('/admin');
}

export function App() {
  const { pathname } = useAppLocation();

  if (pathname.startsWith('/login')) return <AuthSessionProvider><RouteSuspense><LoginPage /></RouteSuspense></AuthSessionProvider>;
  if (isBackofficePath(pathname)) return <AuthSessionProvider><RouteSuspense><ProtectedRoute requiredPermissions={BACKOFFICE_ACCESS_PERMISSIONS}><HostConsolePage /></ProtectedRoute></RouteSuspense></AuthSessionProvider>;

  return <AuthSessionProvider><RouteSuspense><HomePage /></RouteSuspense></AuthSessionProvider>;
}

function RouteSuspense({ children }: { children: ReactNode }) {
  return <Suspense fallback={<main className="routeLoading" aria-busy="true"><span>LiveAuction Studio</span><b>正在加载工作台…</b></main>}>
    {children}
  </Suspense>;
}
