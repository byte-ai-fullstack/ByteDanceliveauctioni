import type { AuthTokens, User } from '../api/types';

export type AuthState = {
  user: User | null;
  tokens: AuthTokens | null;
};

const STORAGE_KEY = 'liveAuction.auth.v1';
const EXPIRED_MESSAGE_KEY = 'liveauction.auth.expiredMessage';

export function readLocalJson<T>(key: string, fallback: T): T {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? JSON.parse(raw) as T : fallback;
  } catch {
    return fallback;
  }
}

export function writeLocalJson<T>(key: string, value: T) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // browser storage can fail in private mode or under quota pressure
  }
}

function removeLocalValue(key: string) {
  try {
    window.localStorage.removeItem(key);
  } catch {
    // ignore storage errors
  }
}

export function loadAuthState(): AuthState {
  const parsed = readLocalJson<AuthState>(STORAGE_KEY, { user: null, tokens: null });
  return { user: parsed.user ?? null, tokens: parsed.tokens ?? null };
}

export function saveAuthState(state: AuthState) {
  writeLocalJson(STORAGE_KEY, state);
  window.dispatchEvent(new Event('auth-state-change'));
}

export function clearAuthState() {
  removeLocalValue(STORAGE_KEY);
  window.dispatchEvent(new Event('auth-state-change'));
}

function readSessionValue(key: string, fallback = '') {
  try {
    return window.sessionStorage.getItem(key) || fallback;
  } catch {
    return fallback;
  }
}

function writeSessionValue(key: string, value: string) {
  try {
    window.sessionStorage.setItem(key, value);
  } catch {
    // ignore storage errors
  }
}

function removeSessionValue(key: string) {
  try {
    window.sessionStorage.removeItem(key);
  } catch {
    // ignore storage errors
  }
}

export function readExpiredMessage(fallback = '登录已过期，请重新登录') {
  return readSessionValue(EXPIRED_MESSAGE_KEY, fallback);
}

export function saveExpiredMessage(message: string) {
  writeSessionValue(EXPIRED_MESSAGE_KEY, message);
}

export function clearExpiredMessage() {
  removeSessionValue(EXPIRED_MESSAGE_KEY);
}
