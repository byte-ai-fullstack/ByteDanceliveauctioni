// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { AppLink } from './AppLink';
import { useAppLocation, useAppNavigate } from './historyStore';

function LocationProbe() {
  const location = useAppLocation();
  return <output>{`${location.pathname}${location.search}${location.hash}`}</output>;
}

function ReplaceButton() {
  const navigate = useAppNavigate();
  return <button type="button" onClick={() => navigate('/admin/orders?status=pending', { replace: true })}>替换地址</button>;
}

beforeEach(() => window.history.replaceState({}, '', '/admin'));

afterEach(() => {
  cleanup();
  window.history.replaceState({}, '', '/');
});

describe('History API 路由', () => {
  it('站内链接更新地址并通知订阅页面', () => {
    render(<><AppLink to="/admin/auctions#queue">进入队列</AppLink><LocationProbe /></>);
    fireEvent.click(screen.getByRole('link', { name: '进入队列' }));
    expect(screen.getByText('/admin/auctions#queue')).toBeTruthy();
  });

  it('编程式导航支持 replace 与查询参数', () => {
    render(<><ReplaceButton /><LocationProbe /></>);
    fireEvent.click(screen.getByRole('button', { name: '替换地址' }));
    expect(screen.getByText('/admin/orders?status=pending')).toBeTruthy();
  });
});
