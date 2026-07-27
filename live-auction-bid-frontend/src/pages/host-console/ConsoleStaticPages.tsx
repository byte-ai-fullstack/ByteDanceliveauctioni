import { Bell, Gavel, ReceiptText, ShieldAlert, Wifi } from 'lucide-react';
import { StudioCard, StudioEmptyState, StudioMetricCard, StudioPageHeader } from './components/studio-ui';

export function SettingsPage() {
  return <section className="settingsPage laSettingsGrid">
    <StudioCard padding="lg" className="laSettingsHero">
      <StudioPageHeader eyebrow="System settings" title="工作台设置" description="P2 只保留后台设置的信息架构入口；涉及风控阈值、默认规则和通知配置的写接口未进入本轮。" />
    </StudioCard>
    <StudioMetricCard icon={<Gavel />} label="默认规则" value="待接口" trend="P3/P4 接配置服务" tone="info" />
    <StudioMetricCard icon={<Wifi />} label="实时链路" value="RoomSocket" trend="统一走 shared/realtime" tone="success" />
    <StudioMetricCard icon={<ReceiptText />} label="成交策略" value="HTTP 查询" trend="订单详情不依赖公开事件" tone="purple" />
    <StudioMetricCard icon={<Bell />} label="通知策略" value="待接口" trend="当前只显示本地提示" tone="warning" />
  </section>;
}

export function AlertsPage() {
  return <StudioCard title="异常告警" subtitle="Alerts" padding="lg" className="alertsPage">
    <StudioEmptyState icon={<ShieldAlert size={34} />} title="告警列表待后端接口" description="P2 不新增 mock 告警数据。竞拍、订单和实时异常已经在对应 feature 页内用真实接口错误展示。" />
  </StudioCard>;
}
