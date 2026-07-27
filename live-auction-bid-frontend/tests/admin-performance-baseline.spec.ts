import { gzipSync } from 'node:zlib';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { expect, test } from '@playwright/test';
import { installAdminMocks, VISUAL_NOW_MS } from './support/adminMock';

const BASELINE_PREFIX = 'ADMIN_BASELINE ';
const ADMIN_JS_BUDGET_BYTES = 447_000;

function report(metric: string, values: Record<string, unknown>) {
  console.log(`${BASELINE_PREFIX}${JSON.stringify({ metric, ...values })}`);
}

function businessReadCount(paths: string[]) {
  return paths.filter((path) => path === '/api/admin/lots'
    || path === '/api/admin/orders'
    || path.endsWith('/snapshot')).length;
}

function sourceFiles(root: string): string[] {
  return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const path = join(root, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|tsx)$/.test(entry.name) ? [path] : [];
  });
}

const liveLot = {
  id: 'lot-baseline-live',
  roomId: 'room-fixture',
  title: '性能基线拍品',
  description: '',
  imageUrl: '',
  status: 'LOT_STATUS_LIVE',
  rule: {
    startPrice: { amount: 10_000, currency: 'CNY' },
    minIncrement: { amount: 100, currency: 'CNY' },
    durationSeconds: 300,
    antiSnipeWindowSeconds: 10,
    antiSnipeExtendSeconds: 10,
    maxExtendCount: 3,
  },
  currentPrice: { amount: 10_000, currency: 'CNY' },
  leadingUserId: 'buyer-baseline',
  leadingNickname: '测试买家',
  startedAtUnixMs: VISUAL_NOW_MS - 10_000,
  endsAtUnixMs: VISUAL_NOW_MS + 290_000,
  settledAtUnixMs: 0,
  winnerUserId: '',
  winnerNickname: '',
  finalPrice: { amount: 0, currency: 'CNY' },
  version: 0,
  trustCards: [],
  duelState: {},
  playbookStage: 'PLAYBOOK_STAGE_WARM_UP',
  stats: { participantCount: 1, bidCount: 1 },
  galleryImageUrls: [],
  tags: [],
};

const liveRoomSnapshot = {
  roomId: 'room-fixture',
  currentLot: liveLot,
  ranking: [],
  recentBids: [],
  playbookStage: 'PLAYBOOK_STAGE_WARM_UP',
  serverTimeUnixMs: VISUAL_NOW_MS,
};

const realtimeRouteScenarios = [
  { label: 'Dashboard', path: '/admin', readySelector: '.merchantDashboardPage' },
  { label: '拍品队列', path: '/admin/auctions', readySelector: '.auctionMgmtPage' },
  { label: '直播中控', path: '/admin/auctions/current/control', readySelector: '.liveControlPage' },
  { label: '出价明细', path: '/admin/bids', readySelector: '.bidAuditPage' },
  { label: '实时诊断', path: '/admin/realtime', readySelector: '.realtimeDiagPage' },
] as const;

test.describe('@baseline 后台 L0 性能基线', () => {
  test.beforeEach(({ page }, testInfo) => {
    void page;
    test.skip(testInfo.project.name !== 'desktop-1440', '性能基线只在固定 1440 视口采样一次');
  });

  test('后台客户端导航不重载文档且复用直播间查询', async ({ page }) => {
    const apiPaths: string[] = [];
    let documentRequests = 0;
    page.on('request', (request) => {
      if (request.resourceType() === 'document') documentRequests += 1;
    });
    await installAdminMocks(page, { onApiRequest: (pathname) => apiPaths.push(pathname) });
    await page.goto('/admin', { waitUntil: 'networkidle' });
    await expect(page.locator('.laAdminShell')).toBeVisible();

    const documentsBefore = documentRequests;
    const roomRequestsBefore = apiPaths.filter((path) => path === '/api/admin/rooms').length;
    await page.getByRole('link', { name: '本场拍品队列', exact: true }).click();
    await page.waitForURL('**/admin/auctions');
    await page.waitForLoadState('networkidle');
    await page.evaluate(() => window.history.back());
    await page.waitForURL('**/admin');
    await page.evaluate(() => window.history.forward());
    await page.waitForURL('**/admin/auctions');

    const metric = {
      documentNavigations: documentRequests - documentsBefore,
      repeatedRoomRequests: apiPaths.filter((path) => path === '/api/admin/rooms').length - roomRequestsBefore,
    };
    report('navigation', metric);
    expect(metric.documentNavigations).toBe(0);
    expect(metric.repeatedRoomRequests).toBe(0);
  });

  for (const scenario of realtimeRouteScenarios) {
    test(`${scenario.label} 每秒 100 条运营快照时不触发业务 HTTP 回源`, async ({ page }) => {
      const apiPaths: string[] = [];
      const mocks = await installAdminMocks(page, {
        onApiRequest: (pathname) => apiPaths.push(pathname),
        roomSnapshot: liveRoomSnapshot,
      });
      await page.goto(scenario.path, { waitUntil: 'networkidle' });
      await mocks.waitForSocket();
      await expect(page.locator(scenario.readySelector)).toBeVisible();
      await page.waitForTimeout(50);

      const businessReadsBefore = businessReadCount(apiPaths);
      const startedAt = performance.now();
      for (let index = 1; index <= 100; index += 1) {
        await mocks.sendSocketMessage({
          messageId: `baseline-${index}`,
          schemaVersion: 1,
          occurredAtUnixMs: VISUAL_NOW_MS + index * 10,
          adminSnapshot: {
            mainAccountId: 'merchant-fixture',
            roomId: 'room-fixture',
            lotId: liveLot.id,
            lotVersion: index,
            status: 'LOT_STATUS_LIVE',
            currentPriceFen: 10_000 + index * 100,
            endsAtUnixMs: liveLot.endsAtUnixMs,
            topRanking: [{
              rank: 1,
              userId: 'buyer-baseline',
              nickname: '测试买家',
              avatarUrl: '',
              amountFen: 10_000 + index * 100,
              bidAtUnixMs: VISUAL_NOW_MS + index * 10,
            }],
          },
        });
        await page.waitForTimeout(10);
      }
      await page.waitForTimeout(50);

      const elapsedSeconds = (performance.now() - startedAt) / 1000;
      const businessReads = businessReadCount(apiPaths) - businessReadsBefore;
      report('realtime-http-qps', {
        route: scenario.path,
        inputEvents: 100,
        elapsedSeconds: Number(elapsedSeconds.toFixed(3)),
        businessReads,
        requestsPerSecond: Number((businessReads / elapsedSeconds).toFixed(2)),
      });
      expect(elapsedSeconds).toBeGreaterThanOrEqual(0.9);
      expect(businessReads / elapsedSeconds).toBeLessThanOrEqual(2);
    });
  }

  test('快速切换订单筛选会中止过期请求', async ({ page }) => {
    await page.addInitScript(() => {
      const nativeFetch = window.fetch.bind(window);
      const state = { started: 0, aborted: 0, inFlight: 0 };
      Object.defineProperty(window, '__ADMIN_ABORT_METRICS__', { configurable: true, value: state });
      window.fetch = (input, init) => {
        const url = String(input instanceof Request ? input.url : input);
        if (!url.includes('/api/admin/orders')) return nativeFetch(input, init);
        state.started += 1;
        state.inFlight += 1;
        return new Promise<Response>((resolve, reject) => {
          let settled = false;
          const finish = () => {
            if (settled) return false;
            settled = true;
            state.inFlight -= 1;
            return true;
          };
          const timer = window.setTimeout(() => {
            nativeFetch(input, init).then(
              (response) => { if (finish()) resolve(response); },
              (error) => { if (finish()) reject(error); },
            );
          }, 250);
          init?.signal?.addEventListener('abort', () => {
            if (!finish()) return;
            window.clearTimeout(timer);
            state.aborted += 1;
            reject(new DOMException('aborted', 'AbortError'));
          }, { once: true });
        });
      };
    });
    await installAdminMocks(page);
    await page.goto('/admin/orders', { waitUntil: 'networkidle' });
    await expect(page.locator('.orderReviewPage')).toBeVisible();
    await page.evaluate(() => {
      const state = (window as unknown as { __ADMIN_ABORT_METRICS__: { started: number; aborted: number; inFlight: number } }).__ADMIN_ABORT_METRICS__;
      state.started = 0;
      state.aborted = 0;
      state.inFlight = 0;
    });

    const status = page.getByLabel('订单状态');
    await status.selectOption('CREATED');
    await page.waitForTimeout(20);
    await status.selectOption('PENDING_PAYMENT');
    await page.waitForTimeout(20);
    await status.selectOption('PAID');
    await page.waitForTimeout(320);

    const metric = await page.evaluate(() => (window as unknown as { __ADMIN_ABORT_METRICS__: { started: number; aborted: number; inFlight: number } }).__ADMIN_ABORT_METRICS__);
    report('rapid-filter-abort', metric);
    expect(metric.started).toBe(3);
    expect(metric.aborted).toBe(2);
    expect(metric.inFlight).toBe(0);
  });

  test('Dashboard 倒计时由叶子时钟驱动且根提交频率受控', async ({ page }) => {
    await page.addInitScript(() => {
      const state = { commits: 0 };
      const renderers = new Map<number, unknown>();
      const hook = {
        renderers,
        supportsFiber: true,
        inject(renderer: unknown) {
          const id = renderers.size + 1;
          renderers.set(id, renderer);
          return id;
        },
        onCommitFiberRoot() {
          state.commits += 1;
        },
        onCommitFiberUnmount() {},
      };
      Object.defineProperty(window, '__REACT_DEVTOOLS_GLOBAL_HOOK__', { configurable: true, value: hook });
      Object.defineProperty(window, '__ADMIN_BASELINE_COMMITS__', { configurable: true, value: state });
    });
    await installAdminMocks(page, { clockMode: 'running' });
    await page.goto('/admin', { waitUntil: 'networkidle' });
    await expect(page.locator('.merchantDashboardPage')).toBeVisible();
    await page.waitForTimeout(250);
    const before = await page.evaluate(() => (window as unknown as { __ADMIN_BASELINE_COMMITS__: { commits: number } }).__ADMIN_BASELINE_COMMITS__.commits);
    const durationSeconds = 3.2;
    await page.waitForTimeout(durationSeconds * 1000);
    const after = await page.evaluate(() => (window as unknown as { __ADMIN_BASELINE_COMMITS__: { commits: number } }).__ADMIN_BASELINE_COMMITS__.commits);
    const rootCommits = after - before;
    report('dashboard-root-commits', {
      durationSeconds,
      rootCommits,
      commitsPerSecond: Number((rootCommits / durationSeconds).toFixed(2)),
    });
    expect(rootCommits).toBeGreaterThanOrEqual(2);
    expect(rootCommits).toBeLessThanOrEqual(5);
  });

  test('请求中止能力、Dashboard 范围与 JS 产物', async () => {
    const projectRoot = process.cwd();
    const httpClient = readFileSync(join(projectRoot, 'src/shared/api/httpClient.ts'), 'utf8');
    const dashboard = readFileSync(join(projectRoot, 'src/features/auction-manage/AdminDashboardPage.tsx'), 'utf8');
    const apiCallSites = sourceFiles(join(projectRoot, 'src')).reduce((total, file) => {
      const source = readFileSync(file, 'utf8');
      return total + (source.match(/\bapiRequest(?:<|\()/g)?.length ?? 0);
    }, 0);
    const supportsAbortSignal = /\bsignal\??\s*:/.test(httpClient) && /\bsignal\s*:/.test(httpClient);
    const dashboardLines = dashboard.split(/\r?\n/).length;
    const dashboardComponents = dashboard.match(/^function\s+[A-Z][A-Za-z0-9_]*/gm)?.length ?? 0;
    const jsDir = join(projectRoot, 'dist/assets');
    const jsAssets = readdirSync(jsDir)
      .filter((name) => name.endsWith('.js'))
      .map((name) => {
        const path = join(jsDir, name);
        const bytes = statSync(path).size;
        return { file: relative(projectRoot, path).replaceAll('\\', '/'), bytes, gzipBytes: gzipSync(readFileSync(path)).length };
      })
      .sort((a, b) => a.file.localeCompare(b.file));
    const metric = {
      apiCallSites,
      abortableCallSites: supportsAbortSignal ? apiCallSites : 0,
      abortCoveragePercent: supportsAbortSignal ? 100 : 0,
      dashboardLines,
      dashboardComponents,
      pageLevelOneSecondTimer: /setInterval|useSecondClock/.test(dashboard),
      jsBytes: jsAssets.reduce((sum, asset) => sum + asset.bytes, 0),
      jsGzipBytes: jsAssets.reduce((sum, asset) => sum + asset.gzipBytes, 0),
      adminJsBytes: jsAssets.filter((asset) => !asset.file.includes('/HomePage-')).reduce((sum, asset) => sum + asset.bytes, 0),
      adminJsBudgetBytes: ADMIN_JS_BUDGET_BYTES,
      jsAssets,
    };
    report('static-and-build', metric);
    expect(metric.apiCallSites).toBeGreaterThan(0);
    expect(metric.jsBytes).toBeGreaterThan(0);
    expect(metric.pageLevelOneSecondTimer).toBe(false);
    expect(metric.adminJsBytes).toBeLessThanOrEqual(ADMIN_JS_BUDGET_BYTES);
  });
});
