/**
 * 全局测试准备。node 与 jsdom 两种环境共用，所以所有浏览器 API 桩都要做存在性判断。
 */
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

afterEach(() => {
  if (typeof document !== 'undefined') cleanup();
});

// jsdom 没有实现 HTMLMediaElement 的播放控制，直播间视图会调用它们。
// 不桩掉会在每次快照渲染时刷一屏 "Not implemented" 噪音。
if (typeof HTMLMediaElement !== 'undefined') {
  const noop = () => {};
  Object.defineProperties(HTMLMediaElement.prototype, {
    play: { configurable: true, writable: true, value: async () => {} },
    pause: { configurable: true, writable: true, value: noop },
    load: { configurable: true, writable: true, value: noop },
  });
}
