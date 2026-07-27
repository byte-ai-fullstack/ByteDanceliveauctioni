import { WS_BASE } from '../config/env';
import type { AuctionEvent, RoomSnapshot } from '../api/types';
import { normalizeRealtimeEnvelope, type RoomHeartbeatV1 } from './realtimeEnvelope';
import { RoomChannel, type RoomChannelResult } from './roomChannel';
import { getWsTicket } from './wsTicket';

export type RoomSocketStatus = 'connecting' | 'connected' | 'reconnecting' | 'disconnected';

export type RoomSocketMeta = {
  lotVersion: number;
  receivedAt: number;
  receivedAtText: string;
  source: 'socket' | 'recover';
};

type RoomSocketOptions = {
  roomId: string;
  heartbeatTimeoutMs?: number;
  recoverSnapshot?: () => Promise<RoomSnapshot | void>;
  onStatusChange?: (status: RoomSocketStatus, attempt: number) => void;
  onEvent?: (event: AuctionEvent, meta: RoomSocketMeta) => void;
  onSnapshot?: (snapshot: RoomSnapshot, meta: RoomSocketMeta) => void;
  onHeartbeat?: (heartbeat: RoomHeartbeatV1, meta: RoomSocketMeta) => void;
  onError?: (error: unknown, phase?: 'ticket' | 'socket' | 'recover' | 'message') => void;
};

function nowText() {
  return new Date().toLocaleTimeString('zh-CN', { hour12: false });
}

export function fullJitterDelay(attempt: number, random = Math.random) {
  const cap = Math.min(500 * 2 ** Math.max(0, attempt - 1), 30_000);
  return Math.floor(Math.max(0, Math.min(1, random())) * cap);
}

function roomURL(roomId: string, ticket: string) {
  const url = new URL(`${WS_BASE}/ws/rooms/${encodeURIComponent(roomId)}`);
  url.searchParams.set('client_app', 'admin-web');
  url.searchParams.set('scope', 'admin');
  url.searchParams.set('ticket', ticket);
  return url.toString();
}

function realtimeConnectionError() {
  const error = new Error('实时连接短暂中断，正在自动重连');
  error.name = 'RealtimeConnectionError';
  return error;
}

export class RoomSocket {
  private readonly options: RoomSocketOptions;
  private socket: WebSocket | null = null;
  private reconnectTimer = 0;
  private heartbeatTimeoutTimer = 0;
  private closed = true;
  private attempt = 0;
  private retryAfterMs = 0;
  private openGeneration = 0;
  private readonly channel: RoomChannel;
  private recoveryPromise: Promise<RoomSnapshot | void> | null = null;
  private recoveryGeneration = 0;
  private messageChain: Promise<void> = Promise.resolve();

  constructor(options: RoomSocketOptions) {
    this.options = options;
    this.channel = new RoomChannel(options.roomId);
  }

  connect() {
    this.closed = false;
    this.open('connecting');
  }

  close() {
    this.closed = true;
    this.openGeneration += 1;
    this.clearTimers();
    const socket = this.socket;
    this.socket = null;
    socket?.close();
    this.emitStatus('disconnected');
  }

  private emitStatus(status: RoomSocketStatus) {
    this.options.onStatusChange?.(status, this.attempt);
  }

  private messageMeta(source: RoomSocketMeta['source'], lotVersion: number): RoomSocketMeta {
    return { lotVersion, source, receivedAt: Date.now(), receivedAtText: nowText() };
  }

  private clearTimers() {
    if (this.reconnectTimer) window.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = 0;
    this.clearHeartbeat();
  }

  private async open(status: RoomSocketStatus) {
    if (this.closed) return;
    const generation = ++this.openGeneration;
    this.emitStatus(status);
    try {
      const ticket = await getWsTicket({ roomId: this.options.roomId, scope: 'admin' });
      if (this.closed || generation !== this.openGeneration) return;
      const socket = new WebSocket(roomURL(this.options.roomId, ticket));
      this.socket = socket;
      this.bindSocket(socket, generation);
    } catch (error) {
      if (this.closed || generation !== this.openGeneration) return;
      this.options.onError?.(error, 'ticket');
      this.scheduleReconnect();
    }
  }

  private bindSocket(socket: WebSocket, generation: number) {
    socket.onopen = () => {
      if (this.closed || this.socket !== socket || generation !== this.openGeneration) return;
      this.attempt = 0;
      this.emitStatus('connected');
      this.markHeartbeat();
      this.messageChain = this.messageChain
        .then(() => this.recoverSnapshot())
        .then(() => undefined)
        .catch((error) => this.options.onError?.(error, 'recover'));
    };
    socket.onmessage = (message) => {
      if (this.closed || this.socket !== socket || generation !== this.openGeneration) return;
      this.markHeartbeat();
      this.messageChain = this.messageChain
        .then(() => {
          if (this.closed || this.socket !== socket || generation !== this.openGeneration) return;
          return this.handleMessage(message.data);
        })
        .catch((error) => this.options.onError?.(error, 'message'));
    };
    socket.onerror = () => {
      if (this.closed || this.socket !== socket || generation !== this.openGeneration) return;
      this.options.onError?.(realtimeConnectionError(), 'socket');
    };
    socket.onclose = () => {
      if (this.closed || this.socket !== socket || generation !== this.openGeneration) return;
      this.socket = null;
      this.clearHeartbeat();
      this.scheduleReconnect();
    };
  }

  private async recoverSnapshot() {
    if (!this.options.recoverSnapshot) return;
    const generation = this.openGeneration;
    if (this.recoveryPromise && this.recoveryGeneration === generation) return this.recoveryPromise;
    const recovery = this.options.recoverSnapshot()
      .then((snapshot) => {
        if (snapshot && !this.closed && generation === this.openGeneration) {
          this.channel.replaceSnapshot(snapshot);
          this.options.onSnapshot?.(snapshot, this.messageMeta('recover', Number(snapshot.currentLot?.version || 0)));
        }
        return snapshot;
      })
      .catch((error) => {
        this.options.onError?.(error, 'recover');
      })
      .finally(() => {
        if (this.recoveryPromise === recovery) this.recoveryPromise = null;
      });
    this.recoveryGeneration = generation;
    this.recoveryPromise = recovery;
    return recovery;
  }

  private scheduleReconnect() {
    if (this.closed || this.reconnectTimer) return;
    this.attempt += 1;
    this.emitStatus('reconnecting');
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = 0;
      this.open('reconnecting');
    }, Math.max(this.retryAfterMs, fullJitterDelay(this.attempt)));
    this.retryAfterMs = 0;
  }

  private markHeartbeat() {
    this.clearHeartbeat();
    this.heartbeatTimeoutTimer = window.setTimeout(() => this.reconnect(), this.options.heartbeatTimeoutMs ?? 15_000);
  }

  private clearHeartbeat() {
    if (this.heartbeatTimeoutTimer) window.clearTimeout(this.heartbeatTimeoutTimer);
    this.heartbeatTimeoutTimer = 0;
  }

  private reconnect() {
    if (this.closed) return;
    this.openGeneration += 1;
    this.clearTimers();
    const socket = this.socket;
    this.socket = null;
    socket?.close();
    this.scheduleReconnect();
  }

  private async handleMessage(data: unknown) {
    try {
      const text = typeof data === 'string'
        ? data
        : data instanceof Blob
          ? await data.text()
          : '';
      if (!text) return;
      const envelope = normalizeRealtimeEnvelope(JSON.parse(text));
      if (envelope.reconnect) {
        this.retryAfterMs = Math.max(0, envelope.reconnect.retryAfterMs);
        this.socket?.close(1013, envelope.reconnect.reason || 'reconnect');
        return;
      }
      if (envelope.heartbeat) {
        const meta = this.messageMeta('socket', envelope.heartbeat.authoritativeLotVersion);
        this.options.onHeartbeat?.(envelope.heartbeat, meta);
      }
      let result = this.channel.ingest(envelope);
      if (result.kind === 'recover') {
        await this.recoverSnapshot();
        result = this.channel.ingest(envelope);
      }
      this.emitChannelResult(result);
    } catch (error) {
      this.options.onError?.(error, 'message');
    }
  }

  private emitChannelResult(result: RoomChannelResult) {
    if (result.kind !== 'applied') return;
    const meta = this.messageMeta('socket', result.lotVersion);
    this.options.onSnapshot?.(result.snapshot, meta);
    this.options.onEvent?.(result.event, meta);
  }

}

export function roomSocketStatusLabel(status: RoomSocketStatus) {
  if (status === 'connected') return '已连接';
  if (status === 'reconnecting') return '重连中';
  if (status === 'disconnected') return '已断开';
  return '连接中';
}
