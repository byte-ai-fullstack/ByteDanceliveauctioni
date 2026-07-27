import { useMemo, type ReactNode } from 'react';
import { ShieldAlert } from 'lucide-react';
import { AdminDashboardPage } from '../../features/auction-manage/AdminDashboardPage';
import { ADMIN_TEAM_ACCOUNT } from '../../shared/config/studio';
import type { Room } from '../../shared/api/types';
import { useAppLocation } from '../../shared/router/historyStore';
import { HostConsoleShell } from './components/HostConsoleShell';
import { StudioCard, StudioEmptyState, StudioPageHeader } from './components/studio-ui';
import { useHostConsolePage } from './model/useHostConsolePage';
import { consoleNavGroups, consoleTitle, findConsoleRoute } from './routes';
import './styles/console-round06.css';

function AppShell({ children, currentRoom }: { children: ReactNode; currentRoom: Room }) {
  const roomSummary = useMemo(() => ({ name: currentRoom.name || currentRoom.id, latency: currentRoom.platform || 'douyin' }), [currentRoom]);
  return <HostConsoleShell navGroups={consoleNavGroups} currentHostRoom={roomSummary} currentTeamAccount={ADMIN_TEAM_ACCOUNT} titleForPath={consoleTitle}>{children}</HostConsoleShell>;
}

export function HostConsolePage() {
  const { room, loading, error } = useHostConsolePage();

  if (loading) return <LoadingShell />;
  if (error || !room) return <RoomErrorPage message={error || '当前主账号还没有可用直播间'} />;
  return <AppShell currentRoom={room}><ConsolePages room={room} /></AppShell>;
}

function ConsolePages({ room }: { room: Room }) {
  const { pathname } = useAppLocation();
  return findConsoleRoute(pathname)?.render(room) ?? <AdminDashboardPage roomId={room.id} roomName={room.name} />;
}

function LoadingShell() {
  return <section className="settingsPage laSettingsGrid"><StudioCard padding="lg"><StudioPageHeader eyebrow="Rooms" title="正在加载直播间" description="正在获取当前主账号的直播间配置。" /></StudioCard></section>;
}

function RoomErrorPage({ message }: { message: string }) {
  return <section className="settingsPage laSettingsGrid"><StudioCard padding="lg"><StudioEmptyState icon={<ShieldAlert size={34} />} title="直播间不可用" description={message} /></StudioCard></section>;
}
