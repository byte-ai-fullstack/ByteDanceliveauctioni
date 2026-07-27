import { useEffect, useState, type ReactNode } from 'react';
import type { StudioTone } from './studio-ui';

export type StudioToastItem = {
  id: string;
  tone?: StudioTone;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
};

export function useStudioToast(timeoutMs = 4200) {
  const [toasts, setToasts] = useState<StudioToastItem[]>([]);
  const showToast = (toast: Omit<StudioToastItem, 'id'> & { id?: string }) => {
    const id = toast.id || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
    setToasts((current) => [...current.filter((item) => item.id !== id), { ...toast, id }]);
    return id;
  };
  const dismissToast = (id: string) => setToasts((current) => current.filter((toast) => toast.id !== id));
  useEffect(() => {
    if (!toasts.length) return;
    const timers = toasts.map((toast) => window.setTimeout(() => dismissToast(toast.id), timeoutMs));
    return () => timers.forEach((timer) => window.clearTimeout(timer));
  }, [toasts, timeoutMs]);
  return { toasts, showToast, dismissToast };
}
