import type { AuctionEvent, RoomSnapshot } from '../api/types';
import { adminSnapshotEvent, heartbeatRequiresRecovery, mergeAdminSnapshot, type RealtimeEnvelopeV1 } from './realtimeEnvelope';

type RoomChannelRecoveryReason = 'missing-snapshot' | 'lot-mismatch' | 'version-gap' | 'heartbeat-mismatch';

export type RoomChannelResult =
  | { kind: 'applied'; snapshot: RoomSnapshot; event: AuctionEvent; lotVersion: number }
  | { kind: 'ignored'; reason: 'other-room' | 'stale' | 'duplicate' | 'non-admin-payload'; lotVersion: number }
  | { kind: 'heartbeat'; lotVersion: number }
  | { kind: 'recover'; reason: RoomChannelRecoveryReason; lotVersion: number };

export class RoomChannel {
  private snapshot: RoomSnapshot | null = null;

  constructor(private readonly roomId: string) {}

  replaceSnapshot(snapshot: RoomSnapshot) {
    if (snapshot.roomId !== this.roomId) throw new Error(`房间快照不匹配：${snapshot.roomId}`);
    this.snapshot = snapshot;
  }

  currentSnapshot() {
    return this.snapshot;
  }

  ingest(envelope: RealtimeEnvelopeV1): RoomChannelResult {
    if (envelope.heartbeat) {
      const lotVersion = envelope.heartbeat.authoritativeLotVersion;
      return heartbeatRequiresRecovery(this.snapshot, envelope.heartbeat)
        ? { kind: 'recover', reason: 'heartbeat-mismatch', lotVersion }
        : { kind: 'heartbeat', lotVersion };
    }

    const incoming = envelope.adminSnapshot;
    if (!incoming) return { kind: 'ignored', reason: 'non-admin-payload', lotVersion: 0 };
    if (incoming.roomId !== this.roomId) return { kind: 'ignored', reason: 'other-room', lotVersion: incoming.lotVersion };

    const currentLot = this.snapshot?.currentLot;
    if (!this.snapshot || !currentLot) return { kind: 'recover', reason: 'missing-snapshot', lotVersion: incoming.lotVersion };
    if (currentLot.id !== incoming.lotId) return { kind: 'recover', reason: 'lot-mismatch', lotVersion: incoming.lotVersion };

    const currentVersion = Number(currentLot.version || 0);
    if (incoming.lotVersion < currentVersion) return { kind: 'ignored', reason: 'stale', lotVersion: incoming.lotVersion };
    if (incoming.lotVersion === currentVersion) return { kind: 'ignored', reason: 'duplicate', lotVersion: incoming.lotVersion };
    if (incoming.lotVersion > currentVersion + 1) return { kind: 'recover', reason: 'version-gap', lotVersion: incoming.lotVersion };

    const merged = mergeAdminSnapshot(this.snapshot, incoming, envelope.occurredAtUnixMs);
    if (!merged) return { kind: 'recover', reason: 'lot-mismatch', lotVersion: incoming.lotVersion };
    this.snapshot = merged;
    return {
      kind: 'applied',
      snapshot: merged,
      event: adminSnapshotEvent(envelope, merged),
      lotVersion: incoming.lotVersion,
    };
  }
}
