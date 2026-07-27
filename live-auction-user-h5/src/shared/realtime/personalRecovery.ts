import { normalizePersonalDeltaV1, type PersonalDeltaV1 } from './realtimeEnvelope';

export type RoomPersonalRecovery = {
  personalDelta: PersonalDeltaV1;
  retryAfterMs: number;
};

export function normalizeRoomPersonalRecovery(input: unknown): RoomPersonalRecovery {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('room personal state reply is invalid');
  const raw = input as Record<string, unknown>;
  const personalState = raw.personalState ?? raw.personal_state;
  const retryAfterMs = Number(raw.retryAfterMs ?? raw.retry_after_ms ?? 0);
  if (!Number.isSafeInteger(retryAfterMs) || retryAfterMs < 0) throw new Error('room personal state retry is invalid');
  return {
    personalDelta: normalizePersonalDeltaV1(personalState),
    retryAfterMs,
  };
}
