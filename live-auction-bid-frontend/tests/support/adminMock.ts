import type { Page, Route, WebSocketRoute } from '@playwright/test';

export const VISUAL_NOW_MS = 1_760_000_000_000;

const ok = { code: 0, message: 'ok' };

const user = {
  id: 'merchant-owner-fixture',
  username: 'merchant-owner',
  nickname: '测试主账号',
  roleCodes: ['merchant_owner'],
  permissionCodes: [
    'team.user.create',
    'team.user.list',
    'team.user.update_role',
    'team.user.update_status',
    'team.user.reset_password',
    'lot.create',
    'lot.update',
    'lot.queue',
    'lot.view_admin',
    'auction.control',
    'order.manage',
    'realtime.view',
    'upload.image',
  ],
  mainAccountId: 'merchant-fixture',
  createdByUserId: 'system',
  status: 'USER_STATUS_ACTIVE',
  createdAtUnixMs: VISUAL_NOW_MS - 86_400_000,
  updatedAtUnixMs: VISUAL_NOW_MS,
};

const room = {
  id: 'room-fixture',
  mainAccountId: 'merchant-fixture',
  name: '测试直播间',
  platform: 'douyin',
  status: 'ACTIVE',
  createdAtUnixMs: VISUAL_NOW_MS - 86_400_000,
  updatedAtUnixMs: VISUAL_NOW_MS,
};

const authState = {
  user,
  tokens: {
    accessToken: 'visual-access-token',
    refreshToken: 'visual-refresh-token',
    accessExpiresAtUnixMs: VISUAL_NOW_MS + 3_600_000,
    refreshExpiresAtUnixMs: VISUAL_NOW_MS + 86_400_000,
  },
};

const emptyRoomSnapshot = {
  roomId: room.id,
  ranking: [],
  recentBids: [],
  playbookStage: 'PLAYBOOK_STAGE_WARM_UP',
  serverTimeUnixMs: VISUAL_NOW_MS,
};

export type AdminMockOptions = {
  clockMode?: 'fixed' | 'running';
  onApiRequest?: (pathname: string) => void;
  roomSnapshot?: Record<string, unknown>;
};

export type AdminMockController = {
  sendSocketMessage: (message: unknown) => Promise<void>;
  waitForSocket: () => Promise<WebSocketRoute>;
};

function paged(items: unknown[], pageSize: number) {
  return { result: ok, total: items.length, page: 1, pageSize, items };
}

async function fulfillApi(route: Route, options: AdminMockOptions) {
  const url = new URL(route.request().url());
  const pathname = url.pathname;
  options.onApiRequest?.(pathname);

  if (pathname === '/api/users/me') {
    await route.fulfill({ json: { result: ok, user } });
    return;
  }
  if (pathname === '/api/admin/rooms') {
    await route.fulfill({ json: { result: ok, rooms: [room] } });
    return;
  }
  if (pathname === '/api/admin/lots') {
    const page = paged([], Number(url.searchParams.get('pageSize') || 20));
    await route.fulfill({ json: { ...page, lots: page.items } });
    return;
  }
  if (pathname === '/api/admin/orders') {
    const page = paged([], Number(url.searchParams.get('pageSize') || 20));
    await route.fulfill({ json: { ...page, orders: page.items } });
    return;
  }
  if (pathname === '/api/admin/users') {
    const page = paged([], Number(url.searchParams.get('pageSize') || 20));
    await route.fulfill({ json: { ...page, users: page.items } });
    return;
  }
  if (pathname === `/api/rooms/${room.id}/snapshot`) {
    await route.fulfill({
      json: {
        result: ok,
        snapshot: options.roomSnapshot ?? emptyRoomSnapshot,
      },
    });
    return;
  }
  if (pathname === '/api/realtime/ws-ticket') {
    await route.fulfill({ json: { result: ok, ticket: 'visual-ws-ticket', scope: 'admin' } });
    return;
  }

  await route.fulfill({
    status: 501,
    json: { result: { code: 501000, message: `visual mock missing for ${pathname}` } },
  });
}

export async function installAdminMocks(page: Page, options: AdminMockOptions = {}): Promise<AdminMockController> {
  if (options.clockMode === 'running') {
    await page.clock.install({ time: VISUAL_NOW_MS });
  } else {
    await page.clock.setFixedTime(VISUAL_NOW_MS);
  }
  await page.addInitScript((state) => {
    window.localStorage.setItem('liveAuction.auth.v1', JSON.stringify(state));
  }, authState);
  await page.route(/^https?:\/\/[^/]+\/api\//, (route) => fulfillApi(route, options));

  let resolveSocket!: (socket: WebSocketRoute) => void;
  const socketReady = new Promise<WebSocketRoute>((resolve) => {
    resolveSocket = resolve;
  });
  await page.routeWebSocket('**/ws/rooms/**', (socket) => {
    resolveSocket(socket);
    socket.onMessage(() => undefined);
  });

  return {
    waitForSocket: () => socketReady,
    sendSocketMessage: async (message) => {
      const socket = await socketReady;
      socket.send(JSON.stringify(message));
    },
  };
}
