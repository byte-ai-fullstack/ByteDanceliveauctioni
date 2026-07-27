import { describe, expect, it } from 'vitest';
import { resultMessage } from './errors';
import { RESULT_CODE } from './types';

describe('resultMessage', () => {
  it('maps username conflicts to a user-facing message', () => {
    expect(resultMessage({ code: RESULT_CODE.USERNAME_TAKEN, message: 'username already exists' })).toBe('用户名已存在，请直接登录或换一个用户名');
  });

  it('does not expose unknown result codes', () => {
    const message = resultMessage({ code: 499999, message: '' });

    expect(message).toBe('请求失败');
    expect(message).not.toContain('499999');
    expect(message).not.toContain('code=');
  });
});
