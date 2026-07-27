import { describe, expect, it } from 'vitest';
import { normalizeRealtimeEnvelope } from './realtimeEnvelope';

const base = { messageId: 'message-1', schemaVersion: 1, occurredAtUnixMs: 1_700_000_000_000 };

describe('protojson 实时信封', () => {
  it('解析后端 camelCase 运营快照并保留授权身份', () => {
    const envelope = normalizeRealtimeEnvelope({
      ...base,
      futureEnvelopeField: 'ignored',
      adminSnapshot: {
        mainAccountId: 'merchant-1', roomId: 'room-1', lotId: 'lot-1', lotVersion: 8, status: 'LOT_STATUS_LIVE',
        currentPriceFen: 18_800, endsAtUnixMs: 1_700_000_030_000,
        topRanking: [{ rank: 1, userId: 'buyer-1', nickname: '真实昵称', avatarUrl: '/avatar.png', amountFen: 18_800, bidAtUnixMs: 1_700_000_000_100, futureRankingField: true }],
      },
    });
    expect(envelope.adminSnapshot).toMatchObject({ lotVersion: 8, topRanking: [{ userId: 'buyer-1', nickname: '真实昵称' }] });
  });

  it('公共快照即使混入身份字段也只产出脱敏字段', () => {
    const envelope = normalizeRealtimeEnvelope({
      ...base,
      publicSnapshot: {
        roomId: 'room-1', lotId: 'lot-1', lotVersion: 8, status: 'LOT_STATUS_LIVE', currentPriceFen: 18_800,
        endsAtUnixMs: 1_700_000_030_000, bidCount: 3,
        topRanking: [{ rank: 1, maskedNickname: '买***', maskedAvatarUrl: '', amountFen: 18_800, bidAtUnixMs: 1_700_000_000_100, userId: 'must-not-leak', nickname: '真实昵称' }],
      },
    });
    expect(envelope.publicSnapshot?.topRanking[0]).toEqual({ rank: 1, maskedNickname: '买***', maskedAvatarUrl: '', amountFen: 18_800, bidAtUnixMs: 1_700_000_000_100 });
    expect(envelope.publicSnapshot?.topRanking[0]).not.toHaveProperty('userId');
    expect(envelope.publicSnapshot?.topRanking[0]).not.toHaveProperty('nickname');
  });

  it('拒绝旧 snake_case 双协议输入', () => {
    expect(() => normalizeRealtimeEnvelope({ message_id: 'legacy', schema_version: 1, occurred_at_unix_ms: 1, heartbeat: {} })).toThrow('不支持的实时协议版本');
  });

  it('拒绝同时携带多个 oneof payload', () => {
    expect(() => normalizeRealtimeEnvelope({
      ...base,
      heartbeat: { lotId: '', authoritativeLotVersion: 0, status: 'LOT_STATUS_UNSPECIFIED', serverTimeUnixMs: 1 },
      reconnect: { retryAfterMs: 500, reason: 'draining' },
    })).toThrow('实时消息必须且只能包含一个 payload');
  });
});
