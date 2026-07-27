import { useEffect, useRef, useState, type ReactNode } from 'react';
import { ArrowDownRight, ArrowUpRight, BarChart3, CircleDollarSign, CreditCard, Gavel, Percent, ReceiptText, TrendingUp, Users } from 'lucide-react';
import type { DashboardAnalytics } from '../model/dashboardAnalytics';
import { calcDelta, easeOutCubic, formatMetricValue, getSparklineGeometry, type MetricFormat } from './dashboardFormat';
import { useInView, usePrefersReducedMotion } from './useViewAnimations';

type MetricDefinition = {
  label: string;
  value: number;
  format: MetricFormat;
  icon: ReactNode;
  tone: 'green' | 'blue' | 'amber' | 'rose' | 'violet' | 'slate';
  trendValues: number[];
};

export function MetricGrid({ analytics }: { analytics: DashboardAnalytics }) {
  const metrics = createMetricCards(analytics);
  return <section className="merchantMetricGrid" aria-label="经营核心指标">
    {metrics.map((metric) => <MetricCard key={metric.label} metric={metric} />)}
  </section>;
}

function MetricCard({ metric }: { metric: MetricDefinition }) {
  const delta = calcDelta(metric.trendValues);
  return <article className={`merchantMetricCard metric-${metric.tone}`}>
    <header>
      <span className="merchantMetricIcon">{metric.icon}</span>
      <TrendBadge delta={delta} />
    </header>
    <p>{metric.label}</p>
    <strong><AnimatedMetricValue value={metric.value} format={metric.format} /></strong>
    <MetricSparkline values={metric.trendValues} />
  </article>;
}

function createMetricCards(analytics: DashboardAnalytics): MetricDefinition[] {
  return [
    { label: 'GMV', value: analytics.gmvYuan, format: 'money', icon: <BarChart3 size={21} />, tone: 'green', trendValues: analytics.timeSeries.map((item) => item.gmv) },
    { label: '已支付金额', value: analytics.paidAmountYuan, format: 'money', icon: <CircleDollarSign size={21} />, tone: 'blue', trendValues: analytics.timeSeries.map((item) => item.paid) },
    { label: '待支付金额', value: analytics.pendingAmountYuan, format: 'money', icon: <CreditCard size={21} />, tone: 'amber', trendValues: analytics.timeSeries.map((item) => item.pending) },
    { label: '订单数', value: analytics.rangeOrders.length, format: 'number', icon: <ReceiptText size={21} />, tone: 'slate', trendValues: analytics.timeSeries.map((item) => item.orders) },
    { label: '支付率', value: analytics.paymentRate, format: 'percent', icon: <Percent size={21} />, tone: 'green', trendValues: analytics.timeSeries.map((item) => item.orders ? (item.paid / Math.max(1, item.gmv)) * 100 : 0) },
    { label: '成交率', value: analytics.dealRate, format: 'percent', icon: <Gavel size={21} />, tone: 'violet', trendValues: analytics.timeSeries.map((_, index) => (index + 1) * analytics.dealRate / Math.max(1, analytics.timeSeries.length)) },
    { label: '参拍人数', value: analytics.participantCount, format: 'number', icon: <Users size={21} />, tone: 'blue', trendValues: analytics.timeSeries.map((item) => item.orders) },
    { label: '平均成交价', value: analytics.averageDealYuan, format: 'money', icon: <TrendingUp size={21} />, tone: 'rose', trendValues: analytics.timeSeries.map((item) => item.orders ? item.gmv / item.orders : 0) },
  ];
}

function TrendBadge({ delta }: { delta: number | null }) {
  if (delta === null) return <span className="merchantTrendBadge flat">暂无趋势</span>;
  const positive = delta >= 0;
  return <span className={`merchantTrendBadge ${positive ? 'up' : 'down'}`}>{positive ? <ArrowUpRight size={14} /> : <ArrowDownRight size={14} />}{positive ? '+' : ''}{delta.toFixed(1)}%</span>;
}

function AnimatedMetricValue({ value, format }: { value: number; format: MetricFormat }) {
  const reduceMotion = usePrefersReducedMotion();
  const [ref, visible] = useInView<HTMLSpanElement>();
  const [displayValue, setDisplayValue] = useState(() => (reduceMotion ? value : 0));
  const previousValue = useRef(reduceMotion ? value : 0);

  useEffect(() => {
    if (reduceMotion) {
      previousValue.current = value;
      return;
    }
    if (!visible) return;
    const startValue = previousValue.current;
    const startedAt = performance.now();
    let animationFrame = 0;
    const tick = (time: number) => {
      const progress = Math.min(1, (time - startedAt) / 980);
      const eased = easeOutCubic(progress);
      setDisplayValue(startValue + (value - startValue) * eased);
      if (progress < 1) {
        animationFrame = window.requestAnimationFrame(tick);
      } else {
        previousValue.current = value;
      }
    };
    animationFrame = window.requestAnimationFrame(tick);
    return () => window.cancelAnimationFrame(animationFrame);
  }, [format, reduceMotion, value, visible]);

  return <span ref={ref} className="merchantAnimatedValue">{formatMetricValue(reduceMotion ? value : displayValue, format)}</span>;
}

function MetricSparkline({ values }: { values: number[] }) {
  const geometry = getSparklineGeometry(values, 112, 38);
  const animationKey = values.map((value) => Number(value.toFixed(3))).join('|');
  return <svg key={animationKey} className="merchantSparkline" viewBox="0 0 112 38" role="img" aria-label="迷你趋势线" focusable="false">
    <path className="merchantSparklineGuide" d={`M3 ${geometry.guideY.toFixed(2)}H109`} />
    <path className="merchantSparklineArea" d={geometry.areaPath} />
    <path className="merchantSparklineLine" d={geometry.linePath} pathLength={1} />
    <circle className="merchantSparklineHalo" cx={geometry.lastPoint.x} cy={geometry.lastPoint.y} r="4.8" />
    <circle className="merchantSparklineDot" cx={geometry.lastPoint.x} cy={geometry.lastPoint.y} r="2.7" />
  </svg>;
}
