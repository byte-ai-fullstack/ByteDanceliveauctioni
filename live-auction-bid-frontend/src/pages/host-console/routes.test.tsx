// @vitest-environment jsdom

import { isValidElement } from 'react';
import { describe, expect, it } from 'vitest';
import type { Room } from '../../shared/api/types';
import { consoleNavGroups, consoleRoutePaths, consoleRoutes, consoleTitle } from './routes';

const room = { id: 'room-route-test', name: '路由测试房间' } as Room;

function concretePath(path: string) {
  return path.replace(':lotId', 'lot-route-test');
}

describe('后台唯一路由表', () => {
  it.each(consoleRoutes)('$id 的标题、导航和页面元素保持自洽', (route) => {
    for (const path of consoleRoutePaths(route)) {
      const pathname = concretePath(path);
      expect(consoleTitle(pathname)).toBe(route.title);
      const activeItems = consoleNavGroups.flatMap((group) => group.items).filter((item) => item.isActive(pathname));
      expect(activeItems).toHaveLength(1);
      expect(activeItems[0]?.href).toBe(route.path);
    }
    expect(isValidElement(route.render(room))).toBe(true);
  });

  it('未知后台路径回退到工作台标题且不误激活导航', () => {
    expect(consoleTitle('/admin/not-found')).toBe('今日工作台');
    expect(consoleNavGroups.flatMap((group) => group.items).some((item) => item.isActive('/admin/not-found'))).toBe(false);
  });
});
