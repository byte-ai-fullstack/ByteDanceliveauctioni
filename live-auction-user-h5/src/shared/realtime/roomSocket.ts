import type { AuctionSocketEvent, RoomSnapshot } from '../api/types';
import { heartbeatRequiresRecovery, mergePublicRealtimeState, normalizeRealtimeEnvelope, publicSnapshotEvent, type PersonalDeltaV1, type RealtimeEnvelopeV1, type RoomSnapshotPublicV1 } from './realtimeEnvelope';
import type { RoomPersonalRecovery } from './personalRecovery';
import { getPublicWsTicket } from './wsTicket';

export type RoomSocketState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'failed' | 'closing';

export type RoomSocketOptions = {
  roomId: string;
  onEvent: (event: AuctionSocketEvent) => void;
  onStateChange?: (state: RoomSocketState) => void;
  onSnapshotRecovery?: () => Promise<RoomSnapshot | void> | RoomSnapshot | void;
  onPersonalRecovery?: () => Promise<RoomPersonalRecovery | void> | RoomPersonalRecovery | void;
  maxReconnectAttempts?: number;
  heartbeatTimeoutMs?: number;
};

function defaultWsBase(): string {
  const currentLocation = globalThis.location;
  if (!currentLocation) return 'ws://localhost';
  return `${currentLocation.protocol === 'https:' ? 'wss' : 'ws'}://${currentLocation.host}`;
}

const WS_BASE = import.meta.env.VITE_WS_BASE || defaultWsBase();

function roomUrl(roomId: string, ticket?: string | null): string {
  const url = new URL(`/ws/rooms/${encodeURIComponent(roomId)}`, WS_BASE);
  url.searchParams.set('scope', 'public');
  if (ticket) url.searchParams.set('ticket', ticket);
  return url.toString();
}

function reconnectDelay(attempt: number): number {
  const cap = Math.min(500 * 2 ** Math.max(0, attempt - 1), 30_000);
  return Math.floor(Math.random() * cap);
}

export class RoomSocket {
  private socket: WebSocket | null = null;
  private state: RoomSocketState = 'idle';
  private stopped = false;
  private reconnectAttempts = 0;
  private reconnectTimer = 0;
  private heartbeatTimeoutTimer = 0;
  private retryAfterMs = 0;
  private personalRetryTimer = 0;
  private personalRetryAttempts = 0;
  private latestSnapshot: RoomSnapshot | null = null;
  private latestPublic: RoomSnapshotPublicV1 | null = null;
  private latestPersonal: PersonalDeltaV1 | undefined;
  private personalStateRevision = 0;
  private recoveryPromise: Promise<RoomSnapshot | void> | null = null;
  private recoveryGeneration = 0;
  private personalRecoveryPromise: Promise<RoomPersonalRecovery | void> | null = null;
  private messageChain: Promise<void> = Promise.resolve();
  private openGeneration = 0;
  private readonly options: RoomSocketOptions;

  constructor(options: RoomSocketOptions) {
    this.options = options;
  }

  getState(): RoomSocketState {
    return this.state;
  }

  connect() {
    if (!this.stopped && this.state !== 'idle' && this.state !== 'failed') return;
    this.stopped = false;
    void this.open();
  }

  disconnect() {
    this.stopped = true;
    this.openGeneration += 1;
    this.setState('closing');
    this.clearTimers();
    this.socket?.close();
    this.socket = null;
    this.setState('idle');
  }

  reconnect() {
    if (this.stopped) return;
    this.openGeneration += 1;
    this.clearTimers();
    const socket = this.socket;
    this.socket = null;
    socket?.close();
    this.scheduleReconnect();
  }

  private async open() {
    const generation = ++this.openGeneration;
    this.setState(this.reconnectAttempts > 0 ? 'reconnecting' : 'connecting');
    try {
      const ticket = await getPublicWsTicket(this.options.roomId).catch(() => null);
      if (this.stopped || generation !== this.openGeneration) return;
      const socket = new WebSocket(roomUrl(this.options.roomId, ticket));
      this.socket = socket;
      this.bindSocket(socket);
    } catch {
      this.scheduleReconnect();
    }
  }

  private bindSocket(socket: WebSocket) {
    socket.onopen = () => {
      if (this.stopped || this.socket !== socket) return;
      this.reconnectAttempts = 0;
      this.setState('connected');
      this.markHeartbeat();
      void this.recoverSnapshot();
    };

    socket.onmessage = (message) => {
      if (this.stopped || this.socket !== socket) return;
      this.markHeartbeat();
      this.messageChain = this.messageChain
        .then(() => {
          if (this.stopped || this.socket !== socket) return;
          return this.handleMessage(String(message.data));
        })
        .catch(() => undefined);
    };

    socket.onerror = () => {
      if (this.stopped || this.socket !== socket) return;
      this.setState(this.reconnectAttempts > 0 ? 'reconnecting' : 'connecting');
    };

    socket.onclose = () => {
      if (this.stopped || this.socket !== socket) return;
      this.socket = null;
      this.clearHeartbeat();
      this.scheduleReconnect();
    };
  }

  private async handleMessage(raw: string) {
    const envelope = normalizeRealtimeEnvelope(JSON.parse(raw));
    if (envelope.reconnect) {
      this.retryAfterMs = Math.max(0, envelope.reconnect.retryAfterMs);
      this.socket?.close(1013, envelope.reconnect.reason || 'reconnect');
      return;
    }
    if (envelope.heartbeat) {
      if (heartbeatRequiresRecovery(this.latestSnapshot, envelope.heartbeat)) await this.recoverSnapshot();
      return;
    }
    if (envelope.adminSnapshot) throw new Error('public socket received admin payload');
    if (envelope.publicSnapshot) {
      await this.handlePublicSnapshot(envelope);
      return;
    }
    if (envelope.personalDelta) await this.handlePersonalDelta(envelope);
  }

  private async handlePublicSnapshot(envelope: RealtimeEnvelopeV1) {
    const incoming = envelope.publicSnapshot!;
    if (incoming.roomId !== this.options.roomId) return;
    if (!this.latestSnapshot || this.recoveryPromise) await this.recoverSnapshot();
    const appliedLot = this.latestSnapshot?.currentLot;
    const appliedVersion = appliedLot?.id === incoming.lotId ? Number(appliedLot.version || 0) : 0;
    const publishedVersion = this.latestPublic?.lotId === incoming.lotId ? this.latestPublic.lotVersion : 0;
    if (incoming.lotVersion < appliedVersion || incoming.lotVersion < publishedVersion) return;
    if (this.latestPublic && this.latestPublic.lotId !== incoming.lotId && this.latestPersonal?.lotId !== incoming.lotId) {
      this.latestPersonal = undefined;
      this.personalStateRevision += 1;
    }
    this.latestPublic = incoming;
    if (this.latestPersonal && (this.latestPersonal.lotId !== incoming.lotId || this.latestPersonal.lotVersion < incoming.lotVersion)) {
      this.latestPersonal = undefined;
      this.personalStateRevision += 1;
    }
    await this.emitMerged(envelope, false);
    if (this.needsPersonalRecovery(incoming)) this.schedulePersonalRecovery(incoming, 100);
  }

  private async handlePersonalDelta(envelope: RealtimeEnvelopeV1) {
    const incoming = envelope.personalDelta!;
    if (incoming.lotId !== this.latestPublic?.lotId) {
      this.latestPersonal = incoming;
      this.personalStateRevision += 1;
      return;
    }
    const publicVersion = this.latestPublic?.lotVersion ?? -1;
    if (incoming.lotVersion < publicVersion) return;
    if (!this.latestPersonal || incoming.lotVersion >= this.latestPersonal.lotVersion) {
      this.latestPersonal = incoming;
      this.personalStateRevision += 1;
    }
    if (incoming.lotVersion > publicVersion || !this.latestPublic) return;
    await this.emitMerged({ ...envelope, publicSnapshot: this.latestPublic, personalDelta: undefined }, true);
    globalThis.clearTimeout(this.personalRetryTimer);
    this.personalRetryTimer = 0;
    this.personalRetryAttempts = 0;
    if (!incoming.tombstone && incoming.orderVisibility === 'ORDER_VISIBILITY_CREATING') {
      this.schedulePersonalRecovery(this.latestPublic, 100);
    }
  }

  private async emitMerged(envelope: RealtimeEnvelopeV1, personalUpdate: boolean) {
    if (!this.latestPublic) return;
    const matchingPersonal = this.latestPersonal?.lotVersion === this.latestPublic.lotVersion ? this.latestPersonal : undefined;
    let merged = mergePublicRealtimeState(this.latestSnapshot, this.latestPublic, matchingPersonal, envelope.occurredAtUnixMs);
    if (!merged) {
      await this.recoverSnapshot();
      merged = mergePublicRealtimeState(this.latestSnapshot, this.latestPublic, matchingPersonal, envelope.occurredAtUnixMs);
    }
    if (!merged) return;
    this.latestSnapshot = merged;
    this.options.onEvent(publicSnapshotEvent({ ...envelope, publicSnapshot: this.latestPublic }, merged, personalUpdate));
  }

  private async recoverSnapshot() {
    if (!this.options.onSnapshotRecovery) return;
    const generation = this.openGeneration;
    if (this.recoveryPromise && this.recoveryGeneration === generation) return this.recoveryPromise;
    const recovery = Promise.resolve(this.options.onSnapshotRecovery())
      .then((snapshot) => {
        if (snapshot && !this.stopped && generation === this.openGeneration) this.latestSnapshot = snapshot;
        return snapshot;
      })
      .finally(() => {
        if (this.recoveryPromise === recovery) this.recoveryPromise = null;
      });
    this.recoveryGeneration = generation;
    this.recoveryPromise = recovery;
    return recovery;
  }

  private needsPersonalRecovery(target: RoomSnapshotPublicV1): boolean {
    const personal = this.latestPersonal;
    if (personal?.lotId !== target.lotId || personal.lotVersion !== target.lotVersion) return true;
    return !personal.tombstone && personal.orderVisibility === 'ORDER_VISIBILITY_CREATING';
  }

  private async recoverPersonalState(target: RoomSnapshotPublicV1) {
    if (!this.options.onPersonalRecovery || this.stopped) return;
    if (this.personalRecoveryPromise) return this.personalRecoveryPromise;
    globalThis.clearTimeout(this.personalRetryTimer);
    this.personalRetryTimer = 0;
    const generation = this.openGeneration;
    const targetLotId = target.lotId;
    const targetVersion = target.lotVersion;
    const startingPersonalRevision = this.personalStateRevision;
    this.personalRecoveryPromise = Promise.resolve(this.options.onPersonalRecovery())
      .then(async (recovery) => {
        if (!recovery || this.stopped || generation !== this.openGeneration) return recovery;
        const currentPublic = this.latestPublic;
        const incoming = recovery.personalDelta;
        if (!currentPublic || currentPublic.lotId !== targetLotId || currentPublic.lotVersion !== targetVersion) return recovery;
        if (this.personalStateRevision !== startingPersonalRevision) return recovery;
        if (incoming.lotId !== targetLotId || incoming.lotVersion !== targetVersion) {
          if (incoming.lotVersion > targetVersion) await this.recoverSnapshot();
          else this.schedulePersonalRecovery(currentPublic, 100);
          return recovery;
        }
        this.personalRetryAttempts = 0;
        this.latestPersonal = incoming;
        this.personalStateRevision += 1;
        await this.emitMerged({
          messageId: `personal-recovery:${targetLotId}:${targetVersion}`,
          schemaVersion: 1,
          occurredAtUnixMs: Date.now(),
          publicSnapshot: currentPublic,
        }, true);
        if (!incoming.tombstone && incoming.orderVisibility === 'ORDER_VISIBILITY_CREATING') {
          this.schedulePersonalRecovery(currentPublic, recovery.retryAfterMs);
        }
        return recovery;
      })
      .catch(() => {
        if (this.stopped || generation !== this.openGeneration) return;
        const currentPublic = this.latestPublic;
        if (currentPublic?.lotId === targetLotId && currentPublic.lotVersion === targetVersion) {
          this.personalRetryAttempts += 1;
          const retryMs = Math.min(1_000 * 2 ** Math.max(0, this.personalRetryAttempts - 1), 10_000);
          this.schedulePersonalRecovery(currentPublic, retryMs);
        }
      })
      .finally(() => {
        this.personalRecoveryPromise = null;
        const currentPublic = this.latestPublic;
        if (currentPublic && (currentPublic.lotId !== targetLotId || currentPublic.lotVersion !== targetVersion) && this.needsPersonalRecovery(currentPublic)) {
          this.schedulePersonalRecovery(currentPublic, 100);
        }
      });
    return this.personalRecoveryPromise;
  }

  private schedulePersonalRecovery(target: RoomSnapshotPublicV1, retryAfterMs: number) {
    if (this.stopped || !this.options.onPersonalRecovery) return;
    globalThis.clearTimeout(this.personalRetryTimer);
    const delay = Math.max(100, Math.min(Number.isFinite(retryAfterMs) ? retryAfterMs : 1_000, 30_000));
    this.personalRetryTimer = globalThis.setTimeout(() => {
      this.personalRetryTimer = 0;
      void this.recoverPersonalState(target);
    }, delay);
  }

  private markHeartbeat() {
    globalThis.clearTimeout(this.heartbeatTimeoutTimer);
    this.heartbeatTimeoutTimer = globalThis.setTimeout(() => this.reconnect(), this.options.heartbeatTimeoutMs ?? 15_000);
  }

  private clearHeartbeat() {
    globalThis.clearTimeout(this.heartbeatTimeoutTimer);
    this.heartbeatTimeoutTimer = 0;
  }

  private clearTimers() {
    this.clearHeartbeat();
    globalThis.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = 0;
    globalThis.clearTimeout(this.personalRetryTimer);
    this.personalRetryTimer = 0;
    this.personalRetryAttempts = 0;
  }

  private scheduleReconnect() {
    if (this.stopped || this.reconnectTimer) return;
    this.clearHeartbeat();
    const maxAttempts = this.options.maxReconnectAttempts ?? Infinity;
    this.reconnectAttempts += 1;
    if (this.reconnectAttempts > maxAttempts) {
      this.setState('failed');
      return;
    }
    this.setState('reconnecting');
    const delay = Math.max(this.retryAfterMs, reconnectDelay(this.reconnectAttempts));
    this.retryAfterMs = 0;
    this.reconnectTimer = globalThis.setTimeout(() => {
      this.reconnectTimer = 0;
      void this.open();
    }, delay);
  }

  private setState(state: RoomSocketState) {
    this.state = state;
    this.options.onStateChange?.(state);
  }
}

export function createRoomSocket(options: RoomSocketOptions): RoomSocket {
  return new RoomSocket(options);
}
