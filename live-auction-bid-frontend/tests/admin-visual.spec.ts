import { expect, test } from '@playwright/test';
import { installAdminMocks } from './support/adminMock';

const routes = [
  { path: '/admin', snapshot: 'dashboard.png' },
  { path: '/admin/auctions/current/control', snapshot: 'live-control.png' },
  { path: '/admin/auctions/create', snapshot: 'auction-create.png' },
  { path: '/admin/auctions', snapshot: 'auction-queue.png' },
  { path: '/admin/auctions/history', snapshot: 'auction-history.png' },
  { path: '/admin/orders', snapshot: 'orders.png' },
  { path: '/admin/bids', snapshot: 'bid-audit.png' },
  { path: '/admin/merchants', snapshot: 'team-accounts.png' },
  { path: '/admin/realtime', snapshot: 'realtime-diagnostics.png' },
  { path: '/admin/settings', snapshot: 'settings.png' },
  { path: '/admin/alerts', snapshot: 'alerts.png' },
] as const;

test.beforeEach(async ({ page }) => {
  await installAdminMocks(page);
});

test.describe('@visual 后台管理路由表现冻结', () => {
  for (const route of routes) {
    test(route.path, async ({ page }) => {
      const pageErrors: string[] = [];
      page.on('pageerror', (error) => pageErrors.push(error.stack || error.message));
      page.on('console', (message) => {
        if (message.type() === 'error') pageErrors.push(message.text());
      });
      page.on('response', (response) => {
        if (response.status() >= 400) pageErrors.push(`${response.status()} ${response.url()}`);
      });
      await page.goto(route.path, { waitUntil: 'networkidle' });
      await expect(
        page.locator('.laAdminShell'),
        `后台壳未出现；url=${page.url()} body=${await page.locator('body').innerText()} errors=${pageErrors.join(' | ')}`,
      ).toBeVisible();
      await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
      await page.evaluate(() => document.fonts.ready);
      await expect(page).toHaveScreenshot(route.snapshot, { fullPage: true });
    });
  }
});
