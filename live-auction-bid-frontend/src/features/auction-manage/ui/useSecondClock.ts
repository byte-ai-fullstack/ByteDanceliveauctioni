import { useSyncExternalStore } from 'react';

const listeners = new Set<() => void>();
let nowMs = Date.now();
let timer = 0;

function subscribe(listener: () => void) {
  listeners.add(listener);
  if (!timer) {
    nowMs = Date.now();
    timer = window.setInterval(() => {
      nowMs = Date.now();
      listeners.forEach((notify) => notify());
    }, 1000);
  }
  return () => {
    listeners.delete(listener);
    if (!listeners.size && timer) {
      window.clearInterval(timer);
      timer = 0;
    }
  };
}

function snapshot() {
  return nowMs;
}

export function useSecondClock() {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
