import type { Money } from '../api/types';

export function formatMoneyText(value?: Money | null, fallback = '未设置') {
  if (!value || value.amount === undefined || value.amount === null || value.amount === '') return fallback;
  const amount = Number(value.amount || 0) / 100;
  const prefix = value.currency === 'CNY' || !value.currency ? '¥' : `${value.currency} `;
  return `${prefix}${amount.toLocaleString('zh-CN', {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`;
}

export function formatAmountText(amount?: number | string | null, currency = 'CNY', fallback = '未设置') {
  if (amount === undefined || amount === null || amount === '') return fallback;
  return formatMoneyText({ amount, currency }, fallback);
}

export function formatDateTimeText(value?: number | string | null, fallback = '未同步') {
  const ts = Number(value || 0);
  if (!Number.isFinite(ts) || ts <= 0) return fallback;
  return new Date(ts).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export function formatDurationText(seconds?: number | string | null) {
  const value = Number(seconds || 0);
  if (!Number.isFinite(value) || value <= 0) return '未设置';
  if (value >= 60) return `${Math.floor(value / 60)} 分钟`;
  return `${value} 秒`;
}
