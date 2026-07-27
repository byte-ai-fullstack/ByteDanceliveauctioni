import type { StudioTone } from '../../../pages/host-console/components/studio-ui';

export type MetricFormat = 'money' | 'number' | 'percent';

const DONUT_SEGMENT_COLORS: Record<StudioTone, string> = {
  success: 'var(--studio-color-success)',
  warning: 'var(--studio-color-warning)',
  danger: 'var(--studio-color-error)',
  info: 'var(--studio-color-info)',
  purple: 'var(--studio-color-purple-text)',
  neutral: 'var(--studio-color-neutral)',
};

const DONUT_EMPTY_COLOR = 'var(--studio-color-neutral-border)';

export function calcDelta(values: number[]) {
  const usable = values.filter((value) => Number.isFinite(value));
  if (usable.length < 2 || usable.every((value) => value === 0)) return null;
  const half = Math.max(1, Math.floor(usable.length / 2));
  const first = usable.slice(0, half).reduce((sum, value) => sum + value, 0);
  const second = usable.slice(half).reduce((sum, value) => sum + value, 0);
  if (first === 0) return second > 0 ? 100 : null;
  return (second - first) / Math.abs(first) * 100;
}

export function getSparklineGeometry(values: number[], width: number, height: number) {
  const safeValues = values.length ? values : [0];
  const max = Math.max(...safeValues);
  const min = Math.min(...safeValues);
  const range = max - min;
  const padding = { top: 5, right: 4, bottom: 7, left: 4 };
  const chartWidth = width - padding.left - padding.right;
  const chartHeight = height - padding.top - padding.bottom;
  const points = safeValues.map((value, index) => {
    const x = safeValues.length === 1 ? width / 2 : padding.left + (chartWidth / (safeValues.length - 1)) * index;
    const y = range === 0 ? padding.top + chartHeight * 0.58 : padding.top + (1 - (value - min) / range) * chartHeight;
    return { x: Number(x.toFixed(2)), y: Number(y.toFixed(2)) };
  });
  const linePath = createSmoothPath(points);
  const firstPoint = points[0];
  const lastPoint = points[points.length - 1];
  const baselineY = height - 4;
  return {
    linePath,
    areaPath: `${linePath} L ${lastPoint.x.toFixed(2)} ${baselineY.toFixed(2)} L ${firstPoint.x.toFixed(2)} ${baselineY.toFixed(2)} Z`,
    guideY: baselineY,
    lastPoint,
  };
}

function createSmoothPath(points: Array<{ x: number; y: number }>) {
  if (points.length === 1) return `M ${points[0].x.toFixed(2)} ${points[0].y.toFixed(2)}`;
  return points.reduce((path, point, index) => {
    if (index === 0) return `M ${point.x.toFixed(2)} ${point.y.toFixed(2)}`;
    const previous = points[index - 1];
    const beforePrevious = points[index - 2] ?? previous;
    const next = points[index + 1] ?? point;
    const controlA = {
      x: previous.x + (point.x - beforePrevious.x) / 6,
      y: previous.y + (point.y - beforePrevious.y) / 6,
    };
    const controlB = {
      x: point.x - (next.x - previous.x) / 6,
      y: point.y - (next.y - previous.y) / 6,
    };
    return `${path} C ${controlA.x.toFixed(2)} ${controlA.y.toFixed(2)}, ${controlB.x.toFixed(2)} ${controlB.y.toFixed(2)}, ${point.x.toFixed(2)} ${point.y.toFixed(2)}`;
  }, '');
}

export function easeOutCubic(progress: number) {
  return 1 - Math.pow(1 - progress, 3);
}

export function formatMetricValue(value: number, format: MetricFormat) {
  if (format === 'money') return formatYuan(value);
  if (format === 'percent') return formatPercent(value);
  return Math.round(value).toLocaleString('zh-CN');
}

export function formatYuan(value: number) {
  return `¥${value.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

export function formatCompactYuan(value: number) {
  if (value >= 10000) return `¥${(value / 10000).toFixed(1)}万`;
  return `¥${Math.round(value).toLocaleString('zh-CN')}`;
}

export function formatPercent(value: number) {
  return `${value.toLocaleString('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 1 })}%`;
}

export function formatOneDecimal(value: number) {
  return value.toLocaleString('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
}

export function formatCountdown(ms: number) {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  return hours > 0 ? `${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}` : `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
}

export function createRiskGradient(items: Array<{ value: number; tone: StudioTone }>) {
  const total = items.reduce((sum, item) => sum + item.value, 0);
  if (!total) return DONUT_EMPTY_COLOR;
  let cursor = 0;
  const parts = items.filter((item) => item.value > 0).map((item) => {
    const start = cursor;
    cursor += item.value / total * 360;
    return `${DONUT_SEGMENT_COLORS[item.tone]} ${start}deg ${cursor}deg`;
  });
  return `conic-gradient(${parts.join(', ')})`;
}
