import { useEffect, useRef, useState } from 'react';
import type { AuctionSocketEvent, RoomSnapshot } from '../../../shared/api/types';
import { createRoomSocket, type RoomSocketState } from '../../../shared/realtime/realtimeClient';
import { getRoomPersonalState } from '../../auction/api/auctionApi';

type SocketState = '连接中' | '已连接' | '重连中' | '已断开';

function toViewState(state: RoomSocketState): SocketState {
  if (state === 'connected') return '已连接';
  if (state === 'connecting') return '连接中';
  if (state === 'failed' || state === 'idle' || state === 'closing') return '已断开';
  return '重连中';
}

export function useAuctionSocket(
  roomId: string,
  onEvent: (event: AuctionSocketEvent) => void,
  onReconnect: () => Promise<RoomSnapshot | void> | RoomSnapshot | void,
  authKey = '',
) {
  const [state, setState] = useState<SocketState>('连接中');
  const onEventRef = useRef(onEvent);
  const reconnectRef = useRef(onReconnect);

  useEffect(() => {
    onEventRef.current = onEvent;
    reconnectRef.current = onReconnect;
  }, [onEvent, onReconnect]);

  useEffect(() => {
    const socket = createRoomSocket({
      roomId,
      onEvent: (event) => onEventRef.current(event),
      onStateChange: (next) => setState(toViewState(next)),
      onSnapshotRecovery: () => reconnectRef.current(),
      onPersonalRecovery: authKey ? () => getRoomPersonalState(roomId) : undefined,
    });

    socket.connect();
    return () => socket.disconnect();
  }, [authKey, roomId]);

  return state;
}
