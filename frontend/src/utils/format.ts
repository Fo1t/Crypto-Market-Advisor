/** Formatting helpers. All of them tolerate undefined so callers stay terse. */

export function formatPrice(value?: number | string | null, digits?: number): string {
  const n = toNumber(value);
  if (n === null) return '—';

  const decimals =
    digits ??
    (Math.abs(n) >= 1000 ? 2 : Math.abs(n) >= 1 ? 4 : Math.abs(n) >= 0.01 ? 6 : 8);
  return n.toLocaleString('en-US', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

export function formatMoney(value?: number | string | null, digits = 2): string {
  const n = toNumber(value);
  if (n === null) return '—';
  return n.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

export function formatPct(value?: number | string | null, digits = 2): string {
  const n = toNumber(value);
  if (n === null) return '—';
  return `${n >= 0 ? '+' : ''}${n.toFixed(digits)}%`;
}

export function formatCompact(value?: number | string | null): string {
  const n = toNumber(value);
  if (n === null) return '—';
  const abs = Math.abs(n);
  if (abs >= 1e12) return `${(n / 1e12).toFixed(2)}T`;
  if (abs >= 1e9) return `${(n / 1e9).toFixed(2)}B`;
  if (abs >= 1e6) return `${(n / 1e6).toFixed(2)}M`;
  if (abs >= 1e3) return `${(n / 1e3).toFixed(2)}K`;
  return n.toFixed(2);
}

export function formatNumber(value?: number | string | null, digits = 2): string {
  const n = toNumber(value);
  if (n === null) return '—';
  return n.toFixed(digits);
}

export function toNumber(value?: number | string | null): number | null {
  if (value === null || value === undefined || value === '') return null;
  const n = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(n) ? n : null;
}

export function formatDateTime(value?: string | null, locale = 'ru'): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString(locale === 'zh-CN' ? 'zh-CN' : locale === 'en' ? 'en-GB' : 'ru-RU', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/** Relative age in a compact form: 3m, 2h, 4d. */
export function formatAge(value?: string | null): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';

  const seconds = Math.max(0, Math.floor((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

export function formatMinutes(minutes?: number | null): string {
  if (minutes === null || minutes === undefined || !Number.isFinite(minutes)) return '—';
  if (minutes < 60) return `${Math.round(minutes)}m`;
  const hours = minutes / 60;
  if (hours < 48) return `${hours.toFixed(1)}h`;
  return `${(hours / 24).toFixed(1)}d`;
}

/** Tone helper: positive numbers read green, negative red. */
export function toneOf(value?: number | string | null): 'long' | 'short' | undefined {
  const n = toNumber(value);
  if (n === null || n === 0) return undefined;
  return n > 0 ? 'long' : 'short';
}

/** Converts an ISO datetime into the value a datetime-local input expects. */
export function toLocalInput(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`;
}
