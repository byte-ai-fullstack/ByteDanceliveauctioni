import { readLocalJson, writeLocalJson } from '../auth/authStorage';

type LogLevel = 'debug' | 'info' | 'warn' | 'error';

type LogEntry = {
  level: LogLevel;
  event: string;
  message?: string;
  requestId?: string;
  ts: string;
  context?: Record<string, unknown>;
};

const LOG_KEY = 'liveauction.client.logs.v1';
const MAX_LOGS = 120;

function readLogs(): LogEntry[] {
  return readLocalJson<LogEntry[]>(LOG_KEY, []);
}

function writeLogs(logs: LogEntry[]) {
  writeLocalJson(LOG_KEY, logs.slice(-MAX_LOGS));
}

function redact(value: unknown): unknown {
  if (!value || typeof value !== 'object') return value;
  if (Array.isArray(value)) return value.map(redact);
  const input = value as Record<string, unknown>;
  const output: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(input)) {
    if (/token|authorization|secret|password|accessKey|secretKey/i.test(key)) {
      output[key] = '[redacted]';
    } else {
      output[key] = redact(item);
    }
  }
  return output;
}

export function createRequestId(prefix = 'web') {
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function clientLog(level: LogLevel, event: string, context?: Record<string, unknown>, message?: string) {
  const entry: LogEntry = {
    level,
    event,
    message,
    requestId: typeof context?.requestId === 'string' ? context.requestId : undefined,
    ts: new Date().toISOString(),
    context: redact(context) as Record<string, unknown> | undefined,
  };
  writeLogs([...readLogs(), entry]);
  const method = level === 'error' ? 'error' : level === 'warn' ? 'warn' : 'log';
  console[method](`[LiveAuction] ${event}`, entry);
}

function getClientLogs() {
  return readLogs();
}

function clearClientLogs() {
  writeLogs([]);
}

// Dev/QA helper: run `window.__LA_LOGS__()` in DevTools.
(globalThis as unknown as { __LA_LOGS__?: () => LogEntry[]; __LA_CLEAR_LOGS__?: () => void }).__LA_LOGS__ = getClientLogs;
(globalThis as unknown as { __LA_LOGS__?: () => LogEntry[]; __LA_CLEAR_LOGS__?: () => void }).__LA_CLEAR_LOGS__ = clearClientLogs;
