import { useState, type CSSProperties } from 'react';
import { BarChart3, ShoppingBag, TrendingUp } from 'lucide-react';
import { StudioEmptyState } from '../../../pages/host-console/components/studio-ui';
import type { FunnelStep, LotPerformance, TimeBucket } from '../model/dashboardAnalytics';
import { formatCompactYuan, formatOneDecimal, formatPercent, formatYuan } from './dashboardFormat';

type CategoryPerformanceRow = {
  label: string;
  count: number;
  paidCount: number;
  amountYuan: number;
  premiumSum: number;
  participantSum: number;
};

export function FunnelChart({ steps, refreshSeq }: { steps: FunnelStep[]; refreshSeq: number }) {
  const max = Math.max(1, ...steps.map((step) => step.value));
  if (!steps.some((step) => step.value > 0)) return <StudioEmptyState compact icon={<BarChart3 size={28} />} title="暂无漏斗数据" description="当前范围没有可统计的拍品。" />;
  return <div className="merchantFunnel" key={refreshSeq}>{steps.map((step, index) => {
    const width = Math.max(8, (step.value / max) * 100);
    return <div className="merchantFunnelRow" key={step.label}>
      <div className="merchantFunnelLabel"><b>{step.label}</b><span>{step.hint}</span></div>
      <div className="merchantFunnelTrack"><span style={{ '--funnel-width': `${width}%`, '--delay': `${index * 80}ms` } as CSSProperties} /></div>
      <strong>{step.value.toLocaleString('zh-CN')}</strong>
    </div>;
  })}</div>;
}

export function TrendChart({ buckets, refreshSeq }: { buckets: TimeBucket[]; refreshSeq: string }) {
  const [activeIndex, setActiveIndex] = useState(Math.max(0, buckets.length - 1));
  if (!buckets.some((bucket) => bucket.gmv || bucket.paid || bucket.pending || bucket.abnormal)) {
    return <StudioEmptyState compact icon={<TrendingUp size={28} />} title="暂无趋势数据" description="当前范围还没有成交订单。" />;
  }
  const width = 760;
  const height = 270;
  const padding = { top: 20, right: 24, bottom: 34, left: 46 };
  const chartWidth = width - padding.left - padding.right;
  const chartHeight = height - padding.top - padding.bottom;
  const maxValue = Math.max(1, ...buckets.map((bucket) => Math.max(bucket.gmv, bucket.paid + bucket.pending + bucket.abnormal)));
  const xFor = (index: number) => padding.left + (buckets.length <= 1 ? chartWidth / 2 : (chartWidth / (buckets.length - 1)) * index);
  const yFor = (value: number) => padding.top + chartHeight - (value / maxValue) * chartHeight;
  const linePoints = buckets.map((bucket, index) => `${xFor(index)},${yFor(bucket.gmv)}`).join(' ');
  const areaPoints = `${padding.left},${padding.top + chartHeight} ${linePoints} ${padding.left + chartWidth},${padding.top + chartHeight}`;
  const active = buckets[activeIndex] ?? buckets[buckets.length - 1];

  return <div className="merchantTrendChart" key={refreshSeq}>
    <div className="merchantLegend">
      <span><i className="gmv" />GMV</span>
      <span><i className="paid" />已支付</span>
      <span><i className="pending" />待支付</span>
      <span><i className="abnormal" />异常</span>
    </div>
    <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label="GMV 和支付趋势图">
      {[0, 0.25, 0.5, 0.75, 1].map((tick) => <g key={tick}>
        <line x1={padding.left} x2={width - padding.right} y1={padding.top + chartHeight * tick} y2={padding.top + chartHeight * tick} />
        <text x={10} y={padding.top + chartHeight * tick + 4}>{formatCompactYuan(maxValue * (1 - tick))}</text>
      </g>)}
      <polygon className="merchantArea" points={areaPoints} />
      {buckets.map((bucket, index) => {
        const x = xFor(index);
        const barWidth = Math.max(8, Math.min(18, chartWidth / Math.max(1, buckets.length) * 0.42));
        let yCursor = padding.top + chartHeight;
        const segments = [
          { key: 'paid', label: '已支付', value: bucket.paid },
          { key: 'pending', label: '待支付', value: bucket.pending },
          { key: 'abnormal', label: '异常', value: bucket.abnormal },
        ];
        return <g key={bucket.label}>
          {segments.map((segment, segmentIndex) => {
            const segmentHeight = (segment.value / maxValue) * chartHeight;
            yCursor -= segmentHeight;
            return <rect
              key={segment.key}
              className={`bar-${segment.key}`}
              x={x - barWidth / 2}
              y={yCursor}
              width={barWidth}
              height={Math.max(0, segmentHeight)}
              rx={3}
              style={{ '--bar-delay': `${index * 70 + segmentIndex * 42}ms` } as CSSProperties}
            >
              <title>{bucket.label} {segment.label}: {formatYuan(segment.value)}</title>
            </rect>;
          })}
          <text x={x} y={height - 10}>{bucket.label}</text>
          <rect className="merchantHoverBand" x={x - chartWidth / Math.max(1, buckets.length) / 2} y={padding.top} width={chartWidth / Math.max(1, buckets.length)} height={chartHeight} onPointerEnter={() => setActiveIndex(index)} />
        </g>;
      })}
      <polyline className="merchantLine" points={linePoints} pathLength={1} />
      {buckets.map((bucket, index) => <circle
        key={`${bucket.label}-point`}
        className="merchantLinePoint"
        cx={xFor(index)}
        cy={yFor(bucket.gmv)}
        r={index === activeIndex ? 5 : 3}
        style={{ '--point-delay': `${520 + index * 45}ms` } as CSSProperties}
      />)}
    </svg>
    <div className="merchantChartTooltip">
      <b>{active?.label}</b>
      <span>GMV {formatYuan(active?.gmv ?? 0)}</span>
      <span>已支付 {formatYuan(active?.paid ?? 0)}</span>
      <span>待支付 {formatYuan(active?.pending ?? 0)}</span>
      <span>异常 {formatYuan(active?.abnormal ?? 0)}</span>
    </div>
  </div>;
}

export function LotRanking({ lots }: { lots: LotPerformance[] }) {
  const max = Math.max(1, ...lots.map((lot) => lot.amountYuan));
  if (!lots.length) return <StudioEmptyState compact icon={<ShoppingBag size={28} />} title="暂无拍品成交额" description="当前范围没有成交拍品。" />;
  return <div className="merchantLotRanking">{lots.slice(0, 10).map((item, index) => <div key={item.lot.id}>
    <span>#{String(index + 1).padStart(2, '0')}</span>
    <div><b>{item.lot.title}</b><small>溢价率 {formatPercent(item.premiumRate)}</small></div>
    <div className="merchantRankBar"><i style={{ width: `${Math.max(5, (item.amountYuan / max) * 100)}%`, '--delay': `${index * 70}ms` } as CSSProperties} /></div>
    <strong>{formatYuan(item.amountYuan)}</strong>
  </div>)}</div>;
}

export function CategoryPerformancePanel({ lots }: { lots: LotPerformance[] }) {
  const rows = buildCategoryPerformance(lots).slice(0, 6);
  if (!rows.length) return <StudioEmptyState compact icon={<BarChart3 size={28} />} title="暂无品类表现" description="当前范围没有可统计的成交品类。" />;
  const totalAmount = rows.reduce((sum, row) => sum + row.amountYuan, 0);
  const totalCount = rows.reduce((sum, row) => sum + row.count, 0);
  const maxAmount = Math.max(1, ...rows.map((row) => row.amountYuan));
  const leader = rows.reduce((best, row) => row.amountYuan > best.amountYuan ? row : best, rows[0]);
  return <div className="merchantCategoryPanel">
    <div className="merchantCategorySummary">
      <div><span>主力品类</span><b>{leader.label}</b><small>{formatYuan(leader.amountYuan)} · {formatPercent(totalAmount ? leader.amountYuan / totalAmount * 100 : 0)}</small></div>
      <div><span>覆盖品类</span><b>{rows.length.toLocaleString('zh-CN')} 类</b><small>{totalCount.toLocaleString('zh-CN')} 件成交，已支付 {rows.reduce((sum, row) => sum + row.paidCount, 0).toLocaleString('zh-CN')} 件</small></div>
    </div>
    <div className="merchantCategoryRows">
      {rows.map((row) => {
        const width = Math.max(5, row.amountYuan / maxAmount * 100);
        const payRate = row.count ? row.paidCount / row.count * 100 : 0;
        return <article key={row.label} className={row.label === leader.label ? 'isLeader' : ''}>
          <header><div><b>{row.label}</b><span>{row.count} 件成交 · 平均参拍 {formatOneDecimal(row.participantSum / row.count)} 人</span></div><strong>{formatYuan(row.amountYuan)}</strong></header>
          <div className="merchantCategoryTrack"><i style={{ '--category-width': `${width}%` } as CSSProperties} /></div>
          <footer><span>支付率 {formatPercent(payRate)}</span><span>平均溢价 {formatPercent(row.premiumSum / row.count)}</span><span>{formatPercent(totalAmount ? row.amountYuan / totalAmount * 100 : 0)} GMV</span></footer>
        </article>;
      })}
    </div>
  </div>;
}

function buildCategoryPerformance(lots: LotPerformance[]): CategoryPerformanceRow[] {
  const rows = new Map<string, CategoryPerformanceRow>();
  lots.forEach((item) => {
    if (item.amountYuan <= 0) return;
    const label = item.lot.category?.trim() || '未分类';
    const row = rows.get(label) ?? { label, count: 0, paidCount: 0, amountYuan: 0, premiumSum: 0, participantSum: 0 };
    row.count += 1;
    row.amountYuan += item.amountYuan;
    row.premiumSum += item.premiumRate;
    row.participantSum += item.participantCount;
    if (item.paid) row.paidCount += 1;
    rows.set(label, row);
  });
  return [...rows.values()].sort((a, b) => b.amountYuan - a.amountYuan);
}
