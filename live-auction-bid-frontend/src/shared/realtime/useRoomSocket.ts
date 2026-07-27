import { useEffect, useRef, useState } from 'react';
import type { AuctionEvent, RoomSnapshot } from '../api/types';
import type { RoomHeartbeatV1 } from './realtimeEnvelope';
import { RoomSocket, roomSocketStatusLabel, type RoomSocketMeta, type RoomSocketStatus } from './roomSocket';

type UseRoomSocketOptions = {
  roomId: string;
  enabled?: boolean;
  recoverSnapshot?: () => Promise<RoomSnapshot | void>;
  onStatusChange?: (status: RoomSocketStatus, attempt: number) => void;
  onEvent?: (event: AuctionEvent, meta: RoomSocketMeta) => void;
  onSnapshot?: (snapshot: RoomSnapshot, meta: RoomSocketMeta) => void;
  onHeartbeat?: (heartbeat: RoomHeartbeatV1, meta: RoomSocketMeta) => void;
  onError?: (error: unknown, phase?: 'ticket' | 'socket' | 'recover' | 'message') => void;
};

type RoomSocketState = {
  status: RoomSocketStatus;
  reconnectCount: number;
  lastEventAt: number | null;
  lastEventAtText: string;
  lastEventType: string;
  lastLotVersion: number;
};

const initialState: RoomSocketState = {
  status: 'connecting',
  reconnectCount: 0,
  lastEventAt: null,
  lastEventAtText: '未收到',
  lastEventType: '暂无',
  lastLotVersion: 0,
};

export function useRoomSocket(options: UseRoomSocketOptions): RoomSocketState {
  const callbacks = useRef(options);
  useEffect(() => {
    callbacks.current = options;
  }, [options]);
  const [state, setState] = useState<RoomSocketState>(initialState);

  useEffect(() => {
    if (options.enabled === false) {
      return;
    }

    let active = true;
    const socket = new RoomSocket({
      roomId: options.roomId,
      recoverSnapshot: () => callbacks.current.recoverSnapshot?.() ?? Promise.resolve(),
      onStatusChange: (status, attempt) => {
        if (!active) return;
        setState((current) => ({
          ...current,
          status,
          reconnectCount: status === 'reconnecting' ? current.reconnectCount + 1 : current.reconnectCount,
        }));
        callbacks.current.onStatusChange?.(status, attempt);
      },
      onEvent: (event, meta) => {
        if (!active) return;
        setState((current) => ({
          ...current,
          lastEventAt: meta.receivedAt,
          lastEventAtText: meta.receivedAtText,
          lastEventType: event.type,
          lastLotVersion: meta.lotVersion,
        }));
        callbacks.current.onEvent?.(event, meta);
      },
      onSnapshot: (snapshot, meta) => {
        if (!active) return;
        setState((current) => ({
          ...current,
          lastEventAt: meta.receivedAt,
          lastEventAtText: meta.receivedAtText,
          lastEventType: meta.source === 'recover' ? 'ROOM_SNAPSHOT' : current.lastEventType,
          lastLotVersion: meta.source === 'recover' ? meta.lotVersion : current.lastLotVersion,
        }));
        callbacks.current.onSnapshot?.(snapshot, meta);
      },
      onHeartbeat: (heartbeat, meta) => {
        if (!active) return;
        setState((current) => ({
          ...current,
          lastEventAt: meta.receivedAt,
          lastEventAtText: meta.receivedAtText,
          lastEventType: 'HEARTBEAT',
          lastLotVersion: meta.lotVersion,
        }));
        callbacks.current.onHeartbeat?.(heartbeat, meta);
      },
      onError: (error, phase) => {
        if (active) callbacks.current.onError?.(error, phase);
      },
    });

    socket.connect();
    return () => {
      active = false;
      socket.close();
    };
  }, [options.roomId, options.enabled]);

  return options.enabled === false ? { ...state, status: 'disconnected' } : state;
}

export { roomSocketStatusLabel };
