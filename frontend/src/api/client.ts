import type {
  AnalysisResponse,
  BacktestFilter,
  HiddenBacktests,
  BacktestRun,
  BacktestTrade,
  Candle,
  CandleCoverage,
  Dashboard,
  EquityPoint,
  Health,
  Market,
  NewsCluster,
  NewsSource,
  NewsStats,
  Page,
  PositionView,
  Recommendation,
  Settings,
  Statistics,
  StrategyCatalogItem,
  StrategyPreset,
} from './types';

/** Error carrying the backend's structured error envelope. */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(message: string, code: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
    this.status = status;
  }
}

const BASE = '/api';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${BASE}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
  });

  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  const body = text ? (JSON.parse(text) as unknown) : null;

  if (!response.ok) {
    const envelope = body as { error?: { message?: string; code?: string } } | null;
    throw new ApiError(
      envelope?.error?.message ?? `HTTP ${response.status}`,
      envelope?.error?.code ?? 'UNKNOWN',
      response.status,
    );
  }
  return body as T;
}

function query(params: Record<string, string | number | boolean | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') {
      search.set(key, String(value));
    }
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : '';
}

export const api = {
  health: () => request<Health>('/health'),
  dashboard: () => request<Dashboard>('/dashboard'),

  markets: (onlyEnabled = false) =>
    request<Page<Market>>(`/markets${query({ enabled: onlyEnabled ? 'true' : undefined })}`),
  market: (symbol: string) => request<Market>(`/markets/${symbol}`),
  createMarket: (payload: {
    coingecko_id: string;
    symbol: string;
    display_name?: string;
    bybit_symbol?: string;
    pinned?: boolean;
  }) => request<Market>('/markets', { method: 'POST', body: JSON.stringify(payload) }),
  updateMarket: (
    symbol: string,
    payload: Partial<{
      enabled: boolean;
      pinned: boolean;
      excluded_from_auto_list: boolean;
      bybit_symbol: string;
      display_name: string;
    }>,
  ) => request<Market>(`/markets/${symbol}`, { method: 'PATCH', body: JSON.stringify(payload) }),
  deleteMarket: (symbol: string) => request<void>(`/markets/${symbol}`, { method: 'DELETE' }),
  refreshUniverse: () => request<{ status: string }>('/markets/refresh', { method: 'POST' }),

  analysis: (symbol: string) => request<AnalysisResponse>(`/markets/${symbol}/analysis`),
  candles: (symbol: string, timeframe: string, limit = 10_000, window?: { from: string; to: string }) =>
    request<{ symbol: string; timeframe: string; candles: Candle[] }>(
      `/markets/${symbol}/candles${query({ timeframe, limit, ...(window ?? {}) })}`,
    ),
  analyzeNow: (symbol: string) =>
    request<{ analysis_id: string; llm_skipped: boolean; llm_error?: string; recommendation?: Recommendation }>(
      `/markets/${symbol}/analyze`,
      { method: 'POST' },
    ),

  news: (params: {
    q?: string; asset?: string; category?: string; source_id?: string; critical?: boolean;
    min_importance?: number; sort?: string; days?: number; limit?: number; offset?: number;
  }) => request<Page<NewsCluster>>(`/news${query(params)}`),
  newsItem: (id: string) => request<NewsCluster>(`/news/${id}`),
  newsStats: () => request<NewsStats>('/news/stats'),
  newsSources: () => request<NewsSource[]>('/news/sources'),
  createNewsSource: (payload: { name: string; url: string; provider: 'rss' | 'atom'; priority: number; enabled?: boolean }) =>
    request<NewsSource>('/news/sources', { method: 'POST', body: JSON.stringify(payload) }),
  updateNewsSource: (id: string, payload: Partial<Pick<NewsSource, 'name' | 'url' | 'provider' | 'priority' | 'enabled'>>) =>
    request<NewsSource>(`/news/sources/${id}`, { method: 'PATCH', body: JSON.stringify(payload) }),
  disableNewsSource: (id: string) => request<void>(`/news/sources/${id}`, { method: 'DELETE' }),

  recommendations: (params: {
    symbol?: string;
    action?: string;
    risk_level?: string;
    min_confidence?: number;
    max_confidence?: number;
    data_quality?: string;
    visibility?: string;
    limit?: number;
    offset?: number;
    days?: number;
  }) =>
    request<Page<Recommendation>>(`/recommendations${query(params)}`),
  recommendation: (id: string) => request<Recommendation>(`/recommendations/${id}`),
  dismissAllRecommendations: () =>
    request<{ dismissed_count: number }>('/recommendations', { method: 'DELETE' }),
  dismissRecommendation: (id: string) => request<void>(`/recommendations/${id}`, { method: 'DELETE' }),
  restoreRecommendation: (id: string) =>
    request<{ status: string }>(`/recommendations/${id}/restore`, { method: 'POST' }),
  decide: (id: string, decision: string, note = '') =>
    request<{ status: string }>(`/recommendations/${id}/decision`, {
      method: 'POST',
      body: JSON.stringify({ decision, note }),
    }),

  positions: (onlyOpen = false) =>
    request<Page<PositionView>>(`/positions${query({ status: onlyOpen ? 'open' : undefined })}`),
  position: (id: string) => request<PositionView>(`/positions/${id}`),
  createPosition: (payload: Record<string, unknown>) =>
    request<PositionView>('/positions', { method: 'POST', body: JSON.stringify(payload) }),
  closePosition: (id: string, payload: Record<string, unknown>) =>
    request<PositionView>(`/positions/${id}/close`, { method: 'POST', body: JSON.stringify(payload) }),
  partialClose: (id: string, payload: Record<string, unknown>) =>
    request<PositionView>(`/positions/${id}/partial-close`, { method: 'POST', body: JSON.stringify(payload) }),
  updatePlan: (id: string, payload: Record<string, unknown>) =>
    request<PositionView>(`/positions/${id}/plan`, { method: 'POST', body: JSON.stringify(payload) }),
  addFee: (id: string, payload: Record<string, unknown>) =>
    request<PositionView>(`/positions/${id}/fee`, { method: 'POST', body: JSON.stringify(payload) }),
  addFunding: (id: string, payload: Record<string, unknown>) =>
    request<PositionView>(`/positions/${id}/funding`, { method: 'POST', body: JSON.stringify(payload) }),
  deletePosition: (id: string) => request<void>(`/positions/${id}`, { method: 'DELETE' }),

  statistics: (days?: number) => request<Statistics>(`/statistics${query({ days })}`),

  backtests: (limit = 25, filter: BacktestFilter = {}) =>
    request<Page<BacktestRun>>(`/backtests${query({ limit, ...filter })}`),
  hiddenBacktests: () => request<HiddenBacktests>('/backtests/hidden'),
  purgeBacktests: () => request<{ removed: number }>('/backtests/purge', { method: 'POST' }),
  hideBacktests: (filter: BacktestFilter = {}) =>
    request<{ hidden: number }>(`/backtests/hide${query({ ...filter })}`, { method: 'POST' }),
  backtest: (id: string) =>
    request<{ run: BacktestRun; trades: BacktestTrade[]; equity_curve?: EquityPoint[] }>(`/backtests/${id}`),
  deleteBacktest: (id: string) => request<void>(`/backtests/${id}`, { method: 'DELETE' }),
  estimateBacktest: (payload: Record<string, unknown>) =>
    request<{
      estimated_inference_count: number;
      requires_confirmation: boolean;
      max_inferences: number;
      coverage?: CandleCoverage;
    }>(
      '/backtests/estimate',
      { method: 'POST', body: JSON.stringify(payload) },
    ),
  createBacktest: (payload: Record<string, unknown>) =>
    request<{ id: string; status: string; estimated_steps: number }>('/backtests', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  cancelBacktest: (id: string) => request<{ status: string }>(`/backtests/${id}/cancel`, { method: 'POST' }),

  strategyCatalog: () =>
    request<{
      items: StrategyCatalogItem[];
      default_min_signal: number;
      presets?: StrategyPreset[];
    }>('/strategies'),
  settings: () => request<Settings>('/settings'),
  updateSettings: (payload: Settings) =>
    request<Settings>('/settings', { method: 'PUT', body: JSON.stringify(payload) }),
};
