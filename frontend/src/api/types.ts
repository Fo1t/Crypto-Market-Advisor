// Types mirroring the Go API DTOs. Machine-readable enums stay in their backend
// form; translation happens in the UI layer only.

export type RecommendationAction = 'OPEN_LONG' | 'OPEN_SHORT' | 'NO_ACTION' | 'MANAGE_POSITION';
export type Direction = 'LONG' | 'SHORT';
export type RiskLevel = 'low' | 'medium' | 'high' | 'extreme' | 'unknown';
export type PositionStatus = 'OPEN' | 'PARTIALLY_CLOSED' | 'CLOSED';
export type Freshness = 'fresh' | 'stale' | 'incomplete';
export type ComponentStatus = 'online' | 'degraded' | 'offline' | 'disabled';
export type Timeframe = '1m' | '5m' | '15m' | '1h' | '4h' | '1d';

export interface Market {
  id: number;
  symbol: string;
  coingecko_id: string;
  display_name: string;
  bybit_symbol: string;
  enabled: boolean;
  manually_added: boolean;
  pinned: boolean;
  excluded_from_auto_list: boolean;
  market_cap_rank?: number;
  price?: number;
  price_change_24h_pct?: number;
  volume_24h?: number;
  market_cap?: number;
  market_updated_at?: string;
  market_regime?: string;
  rsi?: number;
  trend?: string;
  last_action?: RecommendationAction;
  last_confidence?: number;
  last_signal_at?: string;
}

export interface PriceTarget {
  price: number;
  close_pct: number;
  reason?: string;
}

export interface LeveragePlan {
  llm_suggested: number;
  risk_maximum: number;
  recommended: number;
  reason?: string;
  risk_reason?: string;
}

export interface EntryPlan {
  type: string;
  current_price: number;
  preferred_min?: number;
  preferred_max?: number;
}

export interface ManagementAction {
  type: string;
  new_stop_loss?: number;
  new_take_profit?: PriceTarget[];
  close_pct?: number;
  reason?: string;
}

export interface ManagementPlan {
  position_id: string;
  actions: ManagementAction[];
}

export interface RecommendationNarrative {
  summary: string;
  leverage_reason?: string;
  take_profit_reasons: string[];
  stop_loss_reasons: string[];
  management_reasons: string[];
  signals_for: string[];
  signals_against: string[];
  invalidation_conditions: string[];
}

export interface Decision {
  recommendation_id: string;
  decision: string;
  linked_position_id?: string;
  decided_at: string;
  note?: string;
}

export interface Outcome {
  recommendation_id: string;
  evaluated_at: string;
  finalized: boolean;
  price_after_5m?: number;
  price_after_1h?: number;
  price_after_24h?: number;
  max_favorable_excursion_pct?: number;
  max_adverse_excursion_pct?: number;
  status: string;
  ambiguous: boolean;
  ambiguity_reason?: string;
  result?: string;
}

export interface Recommendation {
  id: string;
  analysis_run_id?: string;
  symbol: string;
  created_at: string;
  dismissed_at?: string;
  action: RecommendationAction;
  confidence: number;
  risk_level: RiskLevel;
  summary: string;
  reference_price: string;
  recommended_allocation_pct: string;
  leverage: LeveragePlan;
  entry?: EntryPlan;
  take_profit: PriceTarget[];
  stop_loss: PriceTarget[];
  management?: ManagementPlan;
  signals_for: string[];
  signals_against: string[];
  invalidation_conditions: string[];
  risk_engine_notes?: string[];
  translations?: Partial<Record<'ru' | 'en' | 'zh-CN', RecommendationNarrative>>;
  model_name: string;
  prompt_version: string;
  market_regime: string;
  data_quality: string;
  freshness: Freshness;
  decision?: Decision;
  outcome?: Outcome;
}

export interface Candle {
  open_time: string;
  close_time: string;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
  turnover: number;
  closed: boolean;
  source: string;
  provider: string;
}

export interface Indicators {
  rsi?: number;
  rsi_state?: string;
  macd_hist?: number;
  macd_state?: string;
  adx?: number;
  trend_strength?: string;
  plus_di?: number;
  minus_di?: number;
  atr?: number;
  atr_pct?: number;
  atr_percentile?: number;
  bb_upper?: number;
  bb_middle?: number;
  bb_lower?: number;
  bb_width?: number;
  bb_percent_b?: number;
  realized_volatility?: number;
  relative_volume?: number;
  obv?: number;
  mfi?: number;
  vwap?: number;
  cmf?: number;
  price_vs_ema_50_pct?: number;
  price_vs_ema_200_pct?: number;
  distance_from_high_pct?: number;
  distance_from_low_pct?: number;
  ema?: Record<string, number>;
  sma?: Record<string, number>;
}

export interface Pattern {
  name: string;
  kind: string;
  direction: string;
  strength: number;
  candle_index: number;
  age_candles: number;
  note?: string;
}

export interface Level {
  price: number;
  type: 'support' | 'resistance';
  strength: number;
  touches: number;
  distance_pct: number;
  origin: string;
  timeframe?: string;
}

export interface Divergence {
  indicator: string;
  type: string;
  direction: string;
  strength: number;
  age_candles: number;
}

export interface SignalScores {
  technical_bull_score: number;
  technical_bear_score: number;
  trend_score: number;
  momentum_score: number;
  pattern_score: number;
  volatility_risk_score: number;
  net_score: number;
  deterministic_bias: string;
}

export interface TimeframeAnalysis {
  timeframe: Timeframe;
  candles_used: number;
  last_closed_candle: string;
  close: number;
  indicators: Indicators;
  patterns?: Pattern[];
  chart_patterns?: Pattern[];
  structure: { state: string; description?: string; events?: unknown[] };
  levels?: Level[];
  divergences?: Divergence[];
  regime: { primary: string; tags?: string[]; score: number };
  scores: SignalScores;
  bias: string;
  candle_source_mix?: Record<string, number>;
  candle_provider_mix?: Record<string, number>;
}

export interface FeatureSnapshot {
  schema_version: number;
  timestamp: string;
  latest_closed_candle_timestamp: string;
  symbol: string;
  price: number;
  timeframes: Record<string, TimeframeAnalysis>;
  trend_alignment: {
    bullish: string[];
    bearish: string[];
    neutral: string[];
    alignment_score: number;
    conflicts?: string[];
  };
  market_regime: { primary: string; tags?: string[]; score: number };
  signal_scores: SignalScores;
  support_resistance: Level[];
  data_quality: { status: string; missing_fields: string[]; notes?: string[] };
  news_context?: NewsSnapshot;
}

export type NewsContextStatus = 'ok' | 'available_but_empty' | 'degraded' | 'unavailable' | 'disabled';
export type NewsSourceStatus = 'online' | 'degraded' | 'offline' | 'disabled';

export interface NewsAssetRef { id: number; symbol: string; name: string; confidence: number }
export interface NewsSourceRef { id: string; name: string; priority: number; system: boolean }
export interface NewsCategoryMatch { category: string; confidence: number }
export interface NewsPublication {
  id: string;
  source: NewsSourceRef;
  url: string;
  title: string;
  summary: string;
  language: string;
  published_at: string;
  first_seen_at: string;
}
export interface NewsReaction {
  asset_id: number;
  symbol: string;
  baseline_time?: string;
  baseline_price?: number;
  return_5m_pct?: number;
  return_15m_pct?: number;
  return_1h_pct?: number;
  return_4h_pct?: number;
  return_24h_pct?: number;
  max_up_move_pct?: number;
  max_down_move_pct?: number;
  observed_through?: string;
  status: 'tracking' | 'complete' | 'insufficient_data';
}
export interface NewsCluster {
  id: string;
  canonical_title: string;
  canonical_url: string;
  canonical_summary: string;
  language: string;
  first_published_at: string;
  first_seen_at: string;
  last_seen_at: string;
  importance: number;
  freshness: number;
  critical: boolean;
  source_count: number;
  publication_count: number;
  assets: NewsAssetRef[];
  categories: NewsCategoryMatch[];
  sources: NewsSourceRef[];
  publications?: NewsPublication[];
  reactions: NewsReaction[];
}
export interface NewsSource {
  id: string;
  name: string;
  url: string;
  provider: 'rss' | 'atom' | 'bybit';
  priority: number;
  enabled: boolean;
  system: boolean;
  status: NewsSourceStatus;
  last_attempt_at?: string;
  last_success_at?: string;
  last_error?: string;
  consecutive_errors: number;
}
export interface NewsStats {
  sources_total: number;
  sources_enabled: number;
  sources_by_status: Partial<Record<NewsSourceStatus, number>>;
  items_total: number;
  clusters_total: number;
  critical_total: number;
  last_seen_at?: string;
}
export interface NewsSnapshot {
  status: NewsContextStatus;
  lookback_hours: number;
  asset_specific: unknown[];
  global: unknown[];
  historical_news_context: { status: string; sample_size_1h: number; sample_size_24h: number };
}

export interface AnalysisResponse {
  id: string;
  symbol: string;
  analysis_timestamp: string;
  latest_closed_candle_timestamp?: string;
  price: number;
  features_snapshot: FeatureSnapshot;
  duration_ms: number;
  triggered_by: string;
  strategy_decision?: StrategyDecision;
}

export interface PnL {
  gross_realized_pnl: string;
  net_realized_pnl: string;
  unrealized_pnl: string;
  total_pnl: string;
  fees: string;
  funding: string;
  realized_pnl_pct?: string;
  unrealized_pnl_pct?: string;
  price_change_pct: string;
  leveraged_roi_pct: string;
  roi_on_margin_pct?: string;
  approximate: boolean;
  fees_configured: boolean;
  remaining_pct: string;
}

export interface Fill {
  id: string;
  kind: string;
  quantity?: string;
  close_pct?: string;
  price: string;
  fee: string;
  fee_type: string;
  fee_estimated: boolean;
  realized_pnl: string;
  executed_at: string;
  note?: string;
}

export interface PositionEvent {
  id: number;
  event_type: string;
  payload: Record<string, unknown>;
  occurred_at: string;
}

export interface TradePlan {
  take_profit: PriceTarget[];
  stop_loss: PriceTarget[];
  updated_at: string;
  note?: string;
}

export interface Position {
  id: string;
  symbol: string;
  direction: Direction;
  status: PositionStatus;
  entry_price: string;
  leverage: string;
  initial_quantity?: string;
  remaining_quantity?: string;
  initial_notional?: string;
  initial_margin?: string;
  size_known: boolean;
  opened_at: string;
  closed_at?: string;
  recommendation_id?: string;
  fee_type: string;
  original_plan?: TradePlan;
  current_plan?: TradePlan;
  note?: string;
}

export interface PositionView {
  position: Position;
  fills: Fill[];
  events?: PositionEvent[];
  fee_events?: unknown[];
  funding_events?: unknown[];
  current_price?: number;
  pnl: PnL;
  age_minutes: number;
  result: string;
}

export interface StatBucket {
  key: string;
  count: number;
  wins: number;
  losses: number;
  win_rate: number;
  average_pnl?: number;
  expected_rate?: number;
  calibration_gap?: number;
}

export interface Statistics {
  generated_at: string;
  window: string;
  predictions: number;
  action_counts: Record<string, number>;
  positions_opened: number;
  positions_closed: number;
  outcomes_resolved: number;
  ambiguous_outcomes: number;
  win_rate: number;
  loss_rate: number;
  profit_factor?: number;
  expectancy: number;
  average_pnl: number;
  median_pnl: number;
  max_drawdown: number;
  average_holding_minutes: number;
  average_mfe_pct: number;
  average_mae_pct: number;
  realized_pnl: string;
  by_symbol: StatBucket[];
  by_direction: StatBucket[];
  by_regime: StatBucket[];
  by_confidence: StatBucket[];
  by_leverage: StatBucket[];
  calibration: StatBucket[];
}

export interface ComponentHealth {
  name: string;
  status: ComponentStatus;
  message?: string;
  checked_at: string;
  last_ok?: string;
  llm_context?: LLMContextHealth;
}

export interface LLMContextHealth {
  context_size: number;
  max_output_tokens: number;
  last_prompt_tokens: number;
  peak_prompt_tokens: number;
  used_pct: number;
  last_used_pct: number;
  warn_pct: number;
  critical_pct: number;
  level: 'ok' | 'warning' | 'critical';
  samples: number;
  observed_at?: string;
}

export interface Health {
  status: ComponentStatus;
  components: ComponentHealth[];
  timestamps?: Record<string, string>;
  scheduler?: {
    last_analysis_cycle?: string;
    next_analysis_cycle?: string;
    cycle_running: boolean;
  };
  version: string;
}

export interface Dashboard {
  markets: Market[];
  recent_recommendations: Recommendation[];
  open_positions: PositionView[];
  performance?: Statistics;
  fees_configured: boolean;
}

/** Optional narrowing of the run list. The same shape drives the bulk hide, so
 * "hide what I am looking at" cannot mean something different from what the
 * table shows. */
export interface BacktestFilter {
  mode?: string;
  symbol?: string;
  status?: string;
  timeframe?: string;
}

/** What a purge of hidden runs would remove. Hiding frees nothing: a few
 * thousand replays add up to hundreds of megabytes of simulated trades. */
export interface HiddenBacktests {
  runs: number;
  trades: number;
  bytes: number;
  size: string;
}

export interface BacktestMetrics {
  total_return_pct: number;
  final_capital: string;
  trades: number;
  wins: number;
  losses: number;
  win_rate: number;
  profit_factor?: number;
  expectancy: number;
  max_drawdown_pct: number;
  sharpe?: number;
  average_trade_pct: number;
  average_mfe_pct: number;
  average_mae_pct: number;
  long_trades: number;
  short_trades: number;
  long_win_rate: number;
  short_win_rate: number;
  average_holding_minutes: number;
  total_fees: string;
  total_funding: string;
  degraded_steps: number;
  data_issues?: string[];
  // How many decision points the replay actually evaluated, and the span it
  // covered. A date range that reaches further back than the stored candles
  // replays only the part it has data for.
  analysis_points?: number;
  replay_from?: string;
  replay_to?: string;
  // Why each decision point did or did not become a trade, keyed by machine
  // reason. Without it a run with no trades cannot be told from a broken one.
  decision_reasons?: Record<string, number>;
  unfilled_entries?: number;
  inferences_used: number;
  cache_hits: number;
}

export interface CandleCoverage {
  candles: number;
  from?: string;
  to?: string;
}

export interface EquityPoint {
  t: string;
  e: number;
}

export interface PnLExitStep {
  pnl_pct: number;
  close_pct: number;
}

export interface BacktestParams {
  mode: string;
  symbol: string;
  timeframe: string;
  date_from: string;
  date_to: string;
  analysis_interval?: string;
  initial_capital: string;
  allocation_pct: string;
  leverage: string;
  slippage_pct: string;
  funding_rate_pct?: string;
  maintenance_margin_pct?: string;
  max_open_positions?: number;
  min_confidence: number;
  inference_pause_ms?: number;
  use_cache: boolean;
  break_even_after_tp?: boolean;
  strategies?: StrategySet;
  exit_mode?: string;
  trailing_atr_mult?: string;
  take_profit_ladder?: PnLExitStep[];
  stop_loss_ladder?: PnLExitStep[];
}

export interface BacktestRun {
  id: string;
  mode: string;
  symbol: string;
  timeframe: string;
  date_from: string;
  date_to: string;
  analysis_interval: string;
  status: string;
  params?: BacktestParams;
  metrics?: BacktestMetrics;
  estimated_steps: number;
  completed_steps: number;
  error_message?: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
}

export interface BacktestTrade {
  id: string;
  direction: Direction;
  opened_at: string;
  closed_at?: string;
  entry_price: string;
  exit_price?: string;
  net_pnl: string;
  pnl_pct: number;
  exit_reason: string;
  confidence?: number;
  executions?: BacktestExecution[];
  strategy_votes?: StrategyVote[];
}

export interface BacktestExecution {
  kind: string;
  executed_at: string;
  price: string;
  quantity: string;
  close_pct?: number;
  gross_pnl: string;
  fee: string;
  funding: string;
  fee_type?: string;
}

export interface StrategyConfig {
  id: string;
  enabled: boolean;
  weight: number;
  hard_veto?: boolean;
}

export type StrategySides = 'both' | 'long' | 'short';

export interface StrategySet {
  min_signal: number;
  items: StrategyConfig[];
  regime_adaptive?: boolean;
  sides?: StrategySides;
}

export interface StrategyPreset {
  id: string;
  set: StrategySet;
  profit_factor_4h: number;
  profit_factor_1d: number;
  worst_window_4h: number;
  worst_window_1d: number;
  trades_4h: number;
  trades_1d: number;
  default: boolean;
}

export interface StrategyCatalogItem {
  id: string;
  kind: 'directional' | 'filter';
  default_weight: number;
  default_enabled: boolean;
  default_hard_veto: boolean;
}

export interface StrategyVote {
  id: string;
  kind: 'directional' | 'filter';
  style?: 'trend' | 'reversion' | 'neutral';
  direction?: 'bullish' | 'bearish' | 'neutral';
  blocks?: 'long' | 'short' | 'both';
  strength: number;
  weight: number;
  score: number;
  hard_veto?: boolean;
  detail?: string;
}

export interface StrategyDecision {
  action: string;
  direction?: string;
  confidence: number;
  long_score: number;
  short_score: number;
  net_score: number;
  block_score: number;
  min_signal: number;
  reason: string;
  votes?: StrategyVote[];
  timeframe?: string;
  evaluated_at: string;
}

export interface Settings {
  general: {
    language: string;
    analysis_interval_seconds: number;
    timeframes: string[];
    analysis_enabled: boolean;
  };
  llm: {
    base_url: string;
    model: string;
    timeout_seconds: number;
    temperature: number;
    max_tokens: number;
    context_size: number;
    enabled: boolean;
    max_concurrent_requests: number;
    prompt_version: string;
  };
  risk: {
    min_leverage: number;
    max_leverage: number;
    max_recommended_allocation_pct: string;
    // Always present in a response: the backend fills it from configuration
    // when an older stored document did not have it.
    risk_per_trade_pct: number;
    high_volatility_atr_pct: number;
    extreme_volatility_atr_pct: number;
    min_confidence: number;
    critical_news_max_leverage: number;
    critical_news_high_vol_max_leverage: number;
    critical_news_max_age_seconds: number;
  };
  news: {
    enabled: boolean;
    fetch_interval_seconds: number;
    llm_lookback_hours: number;
    llm_max_asset_items: number;
    llm_max_global_items: number;
    history_min_sample_size: number;
    bybit_enabled: boolean;
  };
  exchange: {
    exchange: string;
    maker_fee_pct: string | null;
    taker_fee_pct: string | null;
    slippage_pct: string;
    fees_configured: boolean;
  };
  strategies: StrategySet;
  updated_at: string;
}

export interface Page<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
}
