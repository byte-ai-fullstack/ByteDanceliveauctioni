import { describe, expect, it } from 'vitest';
import { normalizeRoomPersonalRecovery } from './personalRecovery';

describe('normalizeRoomPersonalRecovery', () => {
  it('accepts camelCase and snake_case personal recovery replies', () => {
    expect(normalizeRoomPersonalRecovery({
      personal_state: {
        user_id: 'buyer-a', lot_id: 'lot-1', lot_version: 8,
        your_rank: 1, your_amount_fen: 12_000, you_are_leading: true,
        your_order_id: 'order-1', order_visibility: 'ORDER_VISIBILITY_CREATING', tombstone: false,
      },
      retry_after_ms: 500,
    })).toEqual({
      personalDelta: {
        userId: 'buyer-a', lotId: 'lot-1', lotVersion: 8,
        yourRank: 1, yourAmountFen: 12_000, youAreLeading: true,
        yourOrderId: 'order-1', orderVisibility: 'ORDER_VISIBILITY_CREATING', tombstone: false,
      },
      retryAfterMs: 500,
    });
  });

  it('rejects invalid retry intervals', () => {
    expect(() => normalizeRoomPersonalRecovery({ personalState: {}, retryAfterMs: -1 })).toThrow('retry');
  });
});
