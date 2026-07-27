import type { Lot } from '../api/types';

export function getServerOffsetMs(serverTimeUnixMs?: number | string): number {
  const serverTime = Number(serverTimeUnixMs || 0);
  if (!Number.isFinite(serverTime) || serverTime <= 0) return 0;
  return serverTime - Date.now();
}

function getServerNowMs(serverTimeUnixMs?: number | string): number {
  return Date.now() + getServerOffsetMs(serverTimeUnixMs);
}

export function getLotLeftMs(lot: Pick<Lot, 'endsAtUnixMs'>, serverTimeUnixMs?: number | string): number {
  const endsAt = Number(lot.endsAtUnixMs || 0);
  if (!Number.isFinite(endsAt) || endsAt <= 0) return 0;
  return Math.max(0, endsAt - getServerNowMs(serverTimeUnixMs));
}

export function formatAuctionLeftMs(leftMs: number, mode: 'queue' | 'control' = 'queue'): string {
  const safeMs = Math.max(0, leftMs);
  const totalSeconds = Math.floor(safeMs / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (mode === 'control') return `${minutes}:${String(seconds).padStart(2, '0')}.${String(safeMs % 1000).padStart(3, '0')}`;
  if (safeMs < 60000) return `${seconds}.${Math.floor((safeMs % 1000) / 100)}`;
  return `${minutes}:${String(seconds).padStart(2, '0')}`;
}
