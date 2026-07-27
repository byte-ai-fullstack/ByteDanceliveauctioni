import { useCallback, useSyncExternalStore } from 'react';

const NAVIGATION_EVENT = 'live-auction:navigate';

function locationSnapshot() {
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

function subscribe(listener: () => void) {
  window.addEventListener('popstate', listener);
  window.addEventListener(NAVIGATION_EVENT, listener);
  return () => {
    window.removeEventListener('popstate', listener);
    window.removeEventListener(NAVIGATION_EVENT, listener);
  };
}

export function navigateApp(to: string, replace = false) {
  const next = new URL(to, window.location.href);
  if (next.origin !== window.location.origin) {
    window.location.assign(next.href);
    return;
  }
  const nextLocation = `${next.pathname}${next.search}${next.hash}`;
  if (nextLocation === locationSnapshot()) return;
  window.history[replace ? 'replaceState' : 'pushState']({}, '', nextLocation);
  window.dispatchEvent(new Event(NAVIGATION_EVENT));
}

export function useAppLocation() {
  const href = useSyncExternalStore(subscribe, locationSnapshot, () => '/');
  const location = new URL(href, window.location.origin);
  return { pathname: location.pathname, search: location.search, hash: location.hash };
}

export function useAppNavigate() {
  return useCallback((to: string, options?: { replace?: boolean }) => navigateApp(to, options?.replace), []);
}
