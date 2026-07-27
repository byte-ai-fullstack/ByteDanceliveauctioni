import type { ReactNode } from 'react';
import { FileClock, Gavel, LayoutDashboard, ListChecks, PlayCircle, Radio, ReceiptText, Settings, ShieldAlert, Users, Wifi } from 'lucide-react';
import { AuctionCreatePage } from '../../features/auction-create/AuctionCreatePage';
import { AdminDashboardPage } from '../../features/auction-manage/AdminDashboardPage';
import { AuctionHistoryPage } from '../../features/auction-manage/AuctionHistoryPage';
import { AuctionManagementPage } from '../../features/auction-manage/AuctionManagementPage';
import { OrderManagementPage } from '../../features/order-manage/OrderManagementPage';
import { BidAuditPage, LiveControlPage, RealtimeDiagnosticsPage } from '../../features/realtime-console/RealtimeConsolePages';
import { TeamAccountsPage } from '../../features/team-accounts/TeamAccountsPage';
import type { Room } from '../../shared/api/types';
import type { StudioNavGroupConfig } from './components/HostConsoleShell';
import { AlertsPage, SettingsPage } from './ConsoleStaticPages';

type NavGroup = '今日直播' | '直播筹备' | '直播后' | '团队协作' | '系统';

type ConsoleRoute = {
  id: string;
  path: string;
  aliases?: string[];
  title: string;
  nav: { group: NavGroup; label: string; icon: ReactNode };
  render: (room: Room) => ReactNode;
};

export const consoleRoutes = [
  { id: 'dashboard', path: '/admin', aliases: ['/host'], title: '今日工作台', nav: { group: '今日直播', label: '今日工作台', icon: <LayoutDashboard size={17} /> }, render: (room) => <AdminDashboardPage roomId={room.id} roomName={room.name} /> },
  { id: 'live-control', path: '/admin/auctions/current/control', aliases: ['/admin/auctions/:lotId/control', '/host/auctions/current/control', '/host/auctions/:lotId/control'], title: '直播间中控台', nav: { group: '今日直播', label: '直播间中控台', icon: <Radio size={17} /> }, render: (room) => <LiveControlPage roomId={room.id} /> },
  { id: 'auction-create', path: '/admin/auctions/create', aliases: ['/host/auctions/create'], title: '添加拍品', nav: { group: '直播筹备', label: '添加拍品', icon: <PlayCircle size={17} /> }, render: (room) => <AuctionCreatePage roomId={room.id} roomName={room.name} /> },
  { id: 'auction-queue', path: '/admin/auctions', aliases: ['/host/auctions'], title: '本场拍品队列', nav: { group: '直播筹备', label: '本场拍品队列', icon: <Gavel size={17} /> }, render: (room) => <AuctionManagementPage roomId={room.id} roomName={room.name} /> },
  { id: 'auction-history', path: '/admin/auctions/history', aliases: ['/host/auctions/history'], title: '拍品历史', nav: { group: '直播后', label: '拍品历史', icon: <FileClock size={17} /> }, render: (room) => <AuctionHistoryPage roomId={room.id} /> },
  { id: 'orders', path: '/admin/orders', aliases: ['/host/orders'], title: '成交处理', nav: { group: '直播后', label: '成交处理', icon: <ReceiptText size={17} /> }, render: () => <OrderManagementPage /> },
  { id: 'bid-audit', path: '/admin/bids', aliases: ['/host/bids'], title: '出价明细', nav: { group: '直播后', label: '出价明细', icon: <ListChecks size={17} /> }, render: (room) => <BidAuditPage roomId={room.id} /> },
  { id: 'team-accounts', path: '/admin/merchants', aliases: ['/host/merchants'], title: '团队成员', nav: { group: '团队协作', label: '团队成员', icon: <Users size={17} /> }, render: () => <TeamAccountsPage /> },
  { id: 'realtime', path: '/admin/realtime', aliases: ['/host/realtime'], title: '直播健康', nav: { group: '团队协作', label: '直播健康', icon: <Wifi size={17} /> }, render: (room) => <RealtimeDiagnosticsPage roomId={room.id} /> },
  { id: 'settings', path: '/admin/settings', aliases: ['/host/settings'], title: '工作台设置', nav: { group: '系统', label: '工作台设置', icon: <Settings size={17} /> }, render: () => <SettingsPage /> },
  { id: 'alerts', path: '/admin/alerts', aliases: ['/host/alerts'], title: '异常告警', nav: { group: '系统', label: '异常告警', icon: <ShieldAlert size={17} /> }, render: () => <AlertsPage /> },
] satisfies ConsoleRoute[];

const groupOrder: NavGroup[] = ['今日直播', '直播筹备', '直播后', '团队协作', '系统'];

export const consoleNavGroups: StudioNavGroupConfig[] = groupOrder.map((label) => ({
  label,
  items: consoleRoutes.filter((route) => route.nav.group === label).map((route) => ({
    label: route.nav.label,
    href: route.path,
    icon: route.nav.icon,
    isActive: (pathname: string) => matchesConsoleRoute(route, pathname),
  })),
}));

export function consoleTitle(pathname: string) {
  return findConsoleRoute(pathname)?.title ?? '今日工作台';
}

export function findConsoleRoute(pathname: string) {
  return consoleRoutes.find((route) => matchesConsoleRoute(route, pathname));
}

export function consoleRoutePaths(route: ConsoleRoute) {
  return [route.path, ...(route.aliases ?? [])];
}

function matchesConsoleRoute(route: ConsoleRoute, pathname: string) {
  return consoleRoutePaths(route).some((path) => pathSegments(path).every((segment, index) => segment.startsWith(':') || segment === pathSegments(pathname)[index]) && pathSegments(path).length === pathSegments(pathname).length);
}

function pathSegments(pathname: string) {
  return pathname.split('/').filter(Boolean);
}
