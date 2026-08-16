import { Fragment, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type {
  BacktestFilter,
  BacktestMetrics,
  BacktestRun,
  BacktestTrade,
  CandleCoverage,
  PnLExitStep,
  StrategySet,
} from '../api/types';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Badge, Card, Stat } from '../components/common';
import { BacktestReport } from '../components/BacktestReport';
import { StrategyEditor } from '../components/StrategyEditor';
import { formatDateTime, formatMinutes, formatMoney, formatNumber, formatPrice, toneOf } from '../utils/format';

const TIMEFRAMES = ['5m', '15m', '1h', '4h', '1d'];

/**
 * parseLadder reads "50:50, 75:25" as "close 50% at +50% on margin, then 25% at
 * +75%". Malformed pairs are dropped here and the backend validates the rest,
 * so a typo cannot silently turn into a different exit rule.
 */
function parseLadder(value: string): PnLExitStep[] {
  return value
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => {
      const [pnl, close] = part.split(':').map((piece) => Number(piece.trim()));
      return { pnl_pct: pnl, close_pct: close };
    })
    .filter((step) => Number.isFinite(step.pnl_pct) && Number.isFinite(step.close_pct));
}

/** formatLadder is the inverse of parseLadder, used when a run is copied back into the form. */
function formatLadder(steps?: PnLExitStep[]): string {
  if (!steps?.length) return '';
  return steps.map((step) => `${step.pnl_pct}:${step.close_pct}`).join(', ');
}

function isoDaysAgo(days: number): string {
  const date = new Date();
  date.setDate(date.getDate() - days);
  return date.toISOString().slice(0, 10);
}

export function BacktestingPage() {
  const { t, i18n } = useTranslation();
  const [filter, setFilter] = useState<BacktestFilter>({});
  const runs = useApi(
    () => api.backtests(25, filter),
    [filter.mode, filter.symbol, filter.status, filter.timeframe],
    5_000,
  );
  const markets = useApi(() => api.markets(true), [], 60_000);
  const [selected, setSelected] = useState<string | null>(null);
  const detail = useApi(
    () => (selected ? api.backtest(selected) : Promise.resolve(null)),
    [selected],
    selected ? 5_000 : 0,
  );

  const [form, setForm] = useState({
    mode: 'technical',
    symbol: 'BTC',
    timeframe: '1h',
    date_from: isoDaysAgo(30),
    date_to: isoDaysAgo(0),
    analysis_interval: '',
    initial_capital: '10000',
    allocation_pct: '5',
    leverage: '10',
    slippage_pct: '0.02',
    funding_rate_pct: '0',
    maintenance_margin_pct: '0',
    max_open_positions: 1,
    min_confidence: 55,
    inference_pause_ms: 0,
    use_cache: true,
    break_even_after_tp: false,
    exit_mode: 'signal',
    trailing_atr_mult: '3',
    take_profit_ladder: '50:50, 75:25, 100:25',
    stop_loss_ladder: '50:100',
  });
  const [estimate, setEstimate] = useState<number | null>(null);
  const [coverage, setCoverage] = useState<CandleCoverage | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [openTrade, setOpenTrade] = useState<string | null>(null);
  // A per-run policy: null means "use whatever the settings screen holds".
  const [policy, setPolicy] = useState<StrategySet | null>(null);
  const settings = useApi(() => api.settings(), []);

  useEffect(() => {
    if (markets.loading) return;
    const available = markets.data?.items ?? [];
    if (available.length === 0) {
      if (form.symbol) setForm((current) => ({ ...current, symbol: '' }));
      return;
    }
    if (available.some((market) => market.symbol === form.symbol)) return;
    setForm((current) => ({ ...current, symbol: available[0].symbol }));
  }, [markets.data, markets.loading, form.symbol]);

  const payload = () => {
    const { take_profit_ladder, stop_loss_ladder, ...rest } = form;
    return {
      ...rest,
      date_from: new Date(`${form.date_from}T00:00:00Z`).toISOString(),
      date_to: new Date(`${form.date_to}T23:59:59Z`).toISOString(),
      ...(policy ? { strategies: policy } : {}),
      ...(form.exit_mode === 'pnl_ladder'
        ? {
            take_profit_ladder: parseLadder(take_profit_ladder),
            stop_loss_ladder: parseLadder(stop_loss_ladder),
          }
        : {}),
    };
  };

  const runEstimate = async () => {
    setBusy(true);
    setMessage(null);
    try {
      const result = await api.estimateBacktest(payload());
      setEstimate(result.estimated_inference_count);
      setCoverage(result.coverage ?? null);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const start = async () => {
    setBusy(true);
    setMessage(null);
    try {
      const result = await api.createBacktest({ ...payload(), confirm: true });
      setSelected(result.id);
      runs.reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const needsConfirmation = form.mode === 'llm';

  /**
   * copyRun refills the form from a previous run. Everything a run needs is
   * already stored with it, so repeating an experiment with one changed
   * parameter does not mean retyping the whole form.
   */
  const copyRun = (run: BacktestRun) => {
    const p = run.params;
    setEstimate(null);
    setPolicy(p?.strategies ? structuredClone(p.strategies) : null);
    setForm((current) => ({
      ...current,
      mode: p?.mode ?? run.mode,
      symbol: p?.symbol ?? run.symbol,
      timeframe: p?.timeframe ?? run.timeframe,
      date_from: (p?.date_from ?? run.date_from).slice(0, 10),
      date_to: (p?.date_to ?? run.date_to).slice(0, 10),
      analysis_interval: p?.analysis_interval ?? run.analysis_interval ?? '',
      initial_capital: p?.initial_capital ?? current.initial_capital,
      allocation_pct: p?.allocation_pct ?? current.allocation_pct,
      leverage: p?.leverage ?? current.leverage,
      slippage_pct: p?.slippage_pct ?? current.slippage_pct,
      funding_rate_pct: p?.funding_rate_pct ?? current.funding_rate_pct,
      maintenance_margin_pct: p?.maintenance_margin_pct ?? current.maintenance_margin_pct,
      max_open_positions: p?.max_open_positions ?? current.max_open_positions,
      min_confidence: p?.min_confidence ?? current.min_confidence,
      inference_pause_ms: p?.inference_pause_ms ?? current.inference_pause_ms,
      use_cache: p?.use_cache ?? current.use_cache,
      break_even_after_tp: p?.break_even_after_tp ?? false,
      exit_mode: p?.exit_mode ?? 'signal',
      trailing_atr_mult: p?.trailing_atr_mult ?? current.trailing_atr_mult,
      take_profit_ladder: formatLadder(p?.take_profit_ladder) || current.take_profit_ladder,
      stop_loss_ladder: formatLadder(p?.stop_loss_ladder) || current.stop_loss_ladder,
    }));
    setMessage(t('backtest.copied', { symbol: run.symbol }));
    window.scrollTo({ top: 0, behavior: 'smooth' });
  };

  const cancelRun = async (run: BacktestRun) => {
    setMessage(null);
    try {
      await api.cancelBacktest(run.id);
      runs.reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : t('backtest.cancelFailed'));
    }
  };

  const removeRun = async (run: BacktestRun) => {
    if (!window.confirm(t('backtest.deleteConfirm', { symbol: run.symbol }))) return;
    setDeleting(run.id);
    setMessage(null);
    try {
      await api.deleteBacktest(run.id);
      if (selected === run.id) setSelected(null);
      runs.reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : t('backtest.deleteFailed'));
    } finally {
      setDeleting(null);
    }
  };

  return (
    <>
      <Card title={t('backtest.newRun')}>
        {needsConfirmation && <div className="banner banner--warn">{t('backtest.warning')}</div>}

        <div className="grid grid--3">
          <label className="field">
            <span className="field__label">{t('backtest.mode')}</span>
            <select value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value })}>
              <option value="technical">{t('backtest.technical')}</option>
              <option value="llm">{t('backtest.llm')}</option>
            </select>
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.symbol')}</span>
            <select
              value={form.symbol}
              disabled={markets.loading || !markets.data?.items.length}
              onChange={(e) => setForm({ ...form, symbol: e.target.value })}
            >
              {markets.loading && <option value={form.symbol}>{t('app.loading')}</option>}
              {!markets.loading && !markets.data?.items.length && <option value="">{t('backtest.noAssets')}</option>}
              {markets.data?.items.map((market) => (
                <option key={market.id} value={market.symbol}>
                  {market.symbol} — {market.display_name}
                </option>
              ))}
            </select>
            {markets.error && <span className="faint short">{t('backtest.assetLoadFailed')}</span>}
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.timeframe')}</span>
            <select value={form.timeframe} onChange={(e) => setForm({ ...form, timeframe: e.target.value })}>
              {TIMEFRAMES.map((tf) => (
                <option key={tf} value={tf}>
                  {tf}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.dateFrom')}</span>
            <input type="date" value={form.date_from} onChange={(e) => setForm({ ...form, date_from: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.dateTo')}</span>
            <input type="date" value={form.date_to} onChange={(e) => setForm({ ...form, date_to: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.analysisInterval')}</span>
            <input
              value={form.analysis_interval}
              onChange={(e) => setForm({ ...form, analysis_interval: e.target.value })}
              placeholder="4h"
            />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.initialCapital')}</span>
            <input value={form.initial_capital} onChange={(e) => setForm({ ...form, initial_capital: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.allocation')}</span>
            <input value={form.allocation_pct} onChange={(e) => setForm({ ...form, allocation_pct: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.leverage')}</span>
            <input value={form.leverage} onChange={(e) => setForm({ ...form, leverage: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.slippage')}</span>
            <input value={form.slippage_pct} onChange={(e) => setForm({ ...form, slippage_pct: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.fundingRate')}</span>
            <input value={form.funding_rate_pct} onChange={(e) => setForm({ ...form, funding_rate_pct: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.maintenanceMargin')}</span>
            <input value={form.maintenance_margin_pct} onChange={(e) => setForm({ ...form, maintenance_margin_pct: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.maxOpenPositions')}</span>
            <input
              type="number"
              min={1}
              max={20}
              value={form.max_open_positions}
              onChange={(e) => setForm({ ...form, max_open_positions: Number(e.target.value) })}
            />
          </label>
          <label className="field field--inline">
            <input
              type="checkbox"
              checked={form.break_even_after_tp}
              onChange={(e) => setForm({ ...form, break_even_after_tp: e.target.checked })}
            />
            <span>{t('backtest.breakEven')}</span>
          </label>
          <label className="field">
            <span className="field__label">{t('backtest.exitMode')}</span>
            <select value={form.exit_mode} onChange={(e) => setForm({ ...form, exit_mode: e.target.value })}>
              <option value="signal">{t('backtest.exitModeSignal')}</option>
              <option value="pnl_ladder">{t('backtest.exitModeLadder')}</option>
              <option value="trailing_atr">{t('backtest.exitModeTrailing')}</option>
            </select>
          </label>
          {form.exit_mode === 'trailing_atr' && (
            <label className="field">
              <span className="field__label">{t('backtest.trailingMult')}</span>
              <input
                value={form.trailing_atr_mult}
                onChange={(e) => setForm({ ...form, trailing_atr_mult: e.target.value })}
              />
              <span className="faint short">{t('backtest.trailingMultHint')}</span>
            </label>
          )}
          {form.exit_mode === 'pnl_ladder' && (
            <>
              <label className="field">
                <span className="field__label">{t('backtest.takeProfitLadder')}</span>
                <input
                  value={form.take_profit_ladder}
                  placeholder="50:50, 75:25, 100:25"
                  onChange={(e) => setForm({ ...form, take_profit_ladder: e.target.value })}
                />
                <span className="faint short">{t('backtest.ladderHint')}</span>
              </label>
              <label className="field">
                <span className="field__label">{t('backtest.stopLossLadder')}</span>
                <input
                  value={form.stop_loss_ladder}
                  placeholder="50:100"
                  onChange={(e) => setForm({ ...form, stop_loss_ladder: e.target.value })}
                />
                <span className="faint short">{t('backtest.ladderLossHint')}</span>
              </label>
            </>
          )}
          <label className="field">
            <span className="field__label">{t('backtest.minConfidence')}</span>
            <input
              type="number"
              value={form.min_confidence}
              onChange={(e) => setForm({ ...form, min_confidence: Number(e.target.value) })}
            />
          </label>
          {form.mode === 'llm' && (
            <label className="field">
              <span className="field__label">{t('backtest.inferencePause')}</span>
              <input
                type="number"
                step={100}
                min={0}
                max={60000}
                value={form.inference_pause_ms}
                onChange={(e) => setForm({ ...form, inference_pause_ms: Number(e.target.value) })}
              />
              <span className="faint short">{t('backtest.inferencePauseHint')}</span>
            </label>
          )}
          <label className="field field--inline">
            <input
              type="checkbox"
              checked={form.use_cache}
              onChange={(e) => setForm({ ...form, use_cache: e.target.checked })}
            />
            <span>{t('backtest.useCache')}</span>
          </label>
        </div>

        {message && <div className="banner banner--error">{message}</div>}
        {estimate !== null && (
          <div className="banner">
            {t('backtest.estimatedInferences')}: <strong>{estimate}</strong>
          </div>
        )}
        {coverage !== null && (
          <CoverageNotice coverage={coverage} from={form.date_from} timeframe={form.timeframe} />
        )}

        <div style={{ marginTop: 12 }}>
          <label className="field field--inline">
            <input
              type="checkbox"
              checked={policy !== null}
              onChange={(e) =>
                setPolicy(
                  e.target.checked
                    ? structuredClone(settings.data?.strategies ?? { min_signal: 1, items: [] })
                    : null,
                )
              }
            />
            <span>{t('backtest.overrideStrategies')}</span>
          </label>
          <p className="faint short">{t('backtest.overrideStrategiesHint')}</p>
          {policy && <StrategyEditor value={policy} onChange={setPolicy} compact />}
        </div>

        <div className="row" style={{ marginTop: 12 }}>
          <button disabled={busy || !form.symbol || !markets.data?.items.length} onClick={() => void runEstimate()}>
            {t('backtest.estimate')}
          </button>
          <button
            className="primary"
            disabled={busy || !form.symbol || !markets.data?.items.length || (needsConfirmation && estimate === null)}
            onClick={() => void start()}
          >
            {needsConfirmation ? t('backtest.confirmRun') : t('backtest.run')}
          </button>
        </div>
      </Card>

      <Card
        title={t('backtest.title')}
        actions={
          <>
            <HideRuns filter={filter} total={runs.data?.total ?? 0} onHidden={runs.reload} />
            <button className="small" onClick={runs.reload}>
              {t('app.refresh')}
            </button>
          </>
        }
      >
        <RunFilters
          filter={filter}
          onChange={setFilter}
          symbols={(markets.data?.items ?? []).map((market) => market.symbol)}
        />
        <AsyncBoundary loading={runs.loading} error={runs.error} onRetry={runs.reload} hasData={!!runs.data}>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('backtest.runId')}</th>
                  <th>{t('backtest.symbol')}</th>
                  <th>{t('backtest.mode')}</th>
                  <th>{t('backtest.timeframe')}</th>
                  <th>{t('backtest.status')}</th>
                  <th>{t('backtest.progress')}</th>
                  <th className="numeric">{t('backtest.trades')}</th>
                  <th className="numeric">{t('backtest.totalReturn')}</th>
                  <th className="numeric">{t('backtest.winRate')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {runs.data?.items.map((run: BacktestRun) => (
                  <tr key={run.id}>
                    <td>
                      <RunId id={run.id} />
                    </td>
                    <td>{run.symbol}</td>
                    <td>{run.mode === 'llm' ? t('backtest.llm') : t('backtest.technical')}</td>
                    <td>{run.timeframe}</td>
                    <td>
                      <Badge
                        tone={
                          run.status === 'completed' ? 'long' : run.status === 'failed' ? 'short' : 'warn'
                        }
                      >
                        {t(`enums.backtestStatus.${run.status}`)}
                      </Badge>
                      {run.error_message && <div className="faint short">{run.error_message}</div>}
                    </td>
                    <td>
                      <BacktestProgress run={run} />
                    </td>
                    <td className="numeric">{run.metrics?.trades ?? '—'}</td>
                    <td className={`numeric ${toneOf(run.metrics?.total_return_pct) ?? ''}`}>
                      {run.metrics ? `${formatNumber(run.metrics.total_return_pct)}%` : '—'}
                    </td>
                    <td className="numeric">
                      {run.metrics ? `${(run.metrics.win_rate * 100).toFixed(0)}%` : '—'}
                    </td>
                    <td>
                      <div className="row">
                        <button className="small" onClick={() => setSelected(run.id)}>
                          {t('backtest.metrics')}
                        </button>
                        <button className="small" onClick={() => copyRun(run)}>
                          {t('backtest.copy')}
                        </button>
                        {(run.status === 'pending' || run.status === 'running') && (
                          <button className="small" onClick={() => void cancelRun(run)}>
                            {t('backtest.cancel')}
                          </button>
                        )}
                        <button
                          className="small danger"
                          disabled={deleting === run.id || run.status === 'pending' || run.status === 'running'}
                          title={run.status === 'pending' || run.status === 'running' ? t('backtest.deleteActiveHint') : undefined}
                          onClick={() => void removeRun(run)}
                        >
                          {t('app.delete')}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
                {runs.data && runs.data.items.length === 0 && (
                  <tr>
                    <td colSpan={9} className="faint">
                      {t('backtest.noRuns')}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </AsyncBoundary>
      </Card>

      {selected && detail.data && (
        <Card
          title={`${detail.data.run.symbol} · ${t('backtest.metrics')}`}
          actions={
            <button className="small ghost" onClick={() => setSelected(null)}>
              {t('app.close')}
            </button>
          }
        >
          <RunId id={detail.data.run.id} full />
          {detail.data.run.metrics && (
            <div className="grid grid--4">
              <Stat
                label={t('backtest.totalReturn')}
                value={`${formatNumber(detail.data.run.metrics.total_return_pct)}%`}
                tone={toneOf(detail.data.run.metrics.total_return_pct)}
              />
              <Stat label={t('backtest.finalCapital')} value={formatMoney(detail.data.run.metrics.final_capital)} />
              <Stat label={t('backtest.trades')} value={detail.data.run.metrics.trades} />
              <Stat label={t('backtest.winRate')} value={`${(detail.data.run.metrics.win_rate * 100).toFixed(0)}%`} />
              <Stat
                label={t('backtest.profitFactor')}
                value={detail.data.run.metrics.profit_factor ? formatNumber(detail.data.run.metrics.profit_factor) : '—'}
              />
              <Stat label={t('backtest.maxDrawdown')} value={`${formatNumber(detail.data.run.metrics.max_drawdown_pct)}%`} />
              <Stat
                label={t('backtest.sharpe')}
                value={detail.data.run.metrics.sharpe !== undefined ? formatNumber(detail.data.run.metrics.sharpe) : '—'}
              />
              <Stat label={t('backtest.avgTrade')} value={`${formatNumber(detail.data.run.metrics.average_trade_pct)}%`} />
              <Stat
                label={t('backtest.longShort')}
                value={`${detail.data.run.metrics.long_trades} / ${detail.data.run.metrics.short_trades}`}
              />
              <Stat label={t('backtest.inferencesUsed')} value={detail.data.run.metrics.inferences_used} />
              <Stat label={t('backtest.cacheHits')} value={detail.data.run.metrics.cache_hits} />
              <Stat label={t('backtest.totalFunding')} value={formatMoney(detail.data.run.metrics.total_funding)} />
              <Stat label={t('statistics.avgMfe')} value={`${formatNumber(detail.data.run.metrics.average_mfe_pct)}%`} />
              <Stat label={t('backtest.degradedSteps')} value={detail.data.run.metrics.degraded_steps ?? 0} />
              <Stat label={t('backtest.analysisPoints')} value={detail.data.run.metrics.analysis_points ?? 0} />
            </div>
          )}

          <DecisionReasons metrics={detail.data.run.metrics} run={detail.data.run} />

          {!!detail.data.run.metrics?.data_issues?.length && (
            <div className="banner banner--warn" style={{ marginTop: 12 }}>
              <strong>{t('backtest.dataIssuesTitle')}</strong>
              <p className="short">{t('backtest.dataIssuesHint')}</p>
              <p className="mono short">{detail.data.run.metrics.data_issues.join(', ')}</p>
            </div>
          )}

          <BacktestReport
            run={detail.data.run}
            trades={detail.data.trades}
            equityCurve={detail.data.equity_curve ?? []}
          />

          <div className="table-wrap" style={{ marginTop: 14 }}>
            <table>
              <thead>
                <tr>
                  <th />
                  <th>{t('positions.openedAt')}</th>
                  <th>{t('positions.direction')}</th>
                  <th className="numeric">{t('positions.entry')}</th>
                  <th className="numeric">{t('backtest.avgExit')}</th>
                  <th className="numeric">P&L</th>
                  <th className="numeric">%</th>
                  <th>{t('backtest.exitReason')}</th>
                  <th className="numeric">{t('backtest.executions')}</th>
                </tr>
              </thead>
              <tbody>
                {detail.data.trades.map((trade: BacktestTrade) => (
                  <Fragment key={trade.id}>
                  <tr>
                    <td>
                      <button
                        className="small ghost"
                        aria-expanded={openTrade === trade.id}
                        aria-label={t('backtest.executions')}
                        onClick={() => setOpenTrade(openTrade === trade.id ? null : trade.id)}
                      >
                        {openTrade === trade.id ? '−' : '+'}
                      </button>
                    </td>
                    <td className="faint">{formatDateTime(trade.opened_at, i18n.language)}</td>
                    <td className={trade.direction === 'LONG' ? 'long' : 'short'}>
                      {t(`enums.direction.${trade.direction}`)}
                    </td>
                    <td className="numeric">{formatPrice(trade.entry_price)}</td>
                    <td className="numeric">{formatPrice(trade.exit_price)}</td>
                    <td className={`numeric ${toneOf(trade.net_pnl) ?? ''}`}>{formatMoney(trade.net_pnl)}</td>
                    <td className={`numeric ${toneOf(trade.pnl_pct) ?? ''}`}>{formatNumber(trade.pnl_pct)}%</td>
                    <td className="faint">
                      {t(`backtest.exitReasons.${trade.exit_reason}`, { defaultValue: trade.exit_reason })}
                    </td>
                    <td className="numeric">{trade.executions?.length ?? 0}</td>
                  </tr>
                  {openTrade === trade.id && (
                    <tr className="subrow">
                      <td colSpan={9}>
                        <TradeExecutions trade={trade} />
                      </td>
                    </tr>
                  )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </>
  );
}

/**
 * BacktestProgress shows how far a run has advanced. An LLM run spends one
 * inference per step and can take a long time, so the remaining time is
 * estimated from the pace of the run so far instead of leaving the user with a
 * bare counter.
 */
function BacktestProgress({ run }: { run: BacktestRun }) {
  const { t } = useTranslation();
  const total = run.estimated_steps;
  const active = run.status === 'running' || run.status === 'pending';

  // A finished run is complete by definition. Its step counter is telemetry the
  // older rows simply do not have, and showing those as 0% would contradict the
  // status next to it.
  if (run.status === 'completed') {
    return (
      <div className="progress" title={`${run.completed_steps} / ${total}`}>
        <div className="progress__track">
          <div className="progress__bar" style={{ width: '100%' }} />
        </div>
        <span className="faint short">100%</span>
      </div>
    );
  }
  if (!total || (!active && run.completed_steps <= 0)) return <span className="faint">—</span>;

  const done = Math.min(run.completed_steps, total);
  const pct = Math.max(0, Math.min(100, Math.round((done / total) * 100)));

  let eta: string | null = null;
  if (active && done > 0 && run.started_at) {
    const elapsedMinutes = (Date.now() - new Date(run.started_at).getTime()) / 60_000;
    if (elapsedMinutes > 0) {
      const remaining = ((total - done) / done) * elapsedMinutes;
      if (Number.isFinite(remaining) && remaining > 0) eta = formatMinutes(remaining);
    }
  }

  return (
    <div className="progress" title={`${done} / ${total}`}>
      <div className="progress__track">
        <div
          className={`progress__bar${active ? ' progress__bar--active' : ''}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="faint short">
        {pct}%{active && eta ? ` · ${t('backtest.eta', { value: eta })}` : ''}
      </span>
    </div>
  );
}

/**
 * TradeExecutions lists the individual fills of one simulated trade. A staged
 * exit is the whole point of a ladder or of multi-level take profits, and the
 * aggregate row above deliberately shows only the quantity-weighted average.
 */
function TradeExecutions({ trade }: { trade: BacktestTrade }) {
  const { t, i18n } = useTranslation();
  const executions = trade.executions ?? [];
  const votes = trade.strategy_votes ?? [];
  if (executions.length === 0) return <p className="faint short">{t('backtest.noExecutions')}</p>;

  return (
    <>
      {votes.length > 0 && (
        <p className="faint short">
          {t('backtest.openedBy')}:{' '}
          {votes
            .filter((vote) => vote.kind === 'directional')
            .map((vote) => `${t(`strategies.${vote.id}`, { defaultValue: vote.id })} ${formatNumber(vote.score, 2)}`)
            .join(' · ')}
        </p>
      )}
    <table className="inner">
      <thead>
        <tr>
          <th>{t('backtest.fillKind')}</th>
          <th>{t('positions.openedAt')}</th>
          <th className="numeric">{t('positions.entry')}</th>
          <th className="numeric">{t('backtest.fillQuantity')}</th>
          <th className="numeric">{t('backtest.fillShare')}</th>
          <th className="numeric">{t('backtest.fillGross')}</th>
          <th className="numeric">{t('backtest.fillFee')}</th>
        </tr>
      </thead>
      <tbody>
        {executions.map((execution, index) => (
          <tr key={`${trade.id}-${index}`}>
            <td>
              {t(`backtest.exitReasons.${execution.kind}`, { defaultValue: execution.kind })}
              {execution.fee_type ? <span className="faint"> · {execution.fee_type}</span> : null}
            </td>
            <td className="faint">{formatDateTime(execution.executed_at, i18n.language)}</td>
            <td className="numeric">{formatPrice(execution.price)}</td>
            <td className="numeric">{formatNumber(execution.quantity, 6)}</td>
            <td className="numeric">{execution.close_pct ? `${formatNumber(execution.close_pct, 0)}%` : '—'}</td>
            <td className={`numeric ${toneOf(execution.gross_pnl) ?? ''}`}>
              {execution.kind === 'funding' ? formatMoney(execution.funding) : formatMoney(execution.gross_pnl)}
            </td>
            <td className="numeric faint">{formatMoney(execution.fee)}</td>
          </tr>
        ))}
      </tbody>
    </table>
    </>
  );
}

/**
 * CoverageNotice warns before a run starts that the requested range reaches
 * further back than the stored candles.
 *
 * The estimated step count is computed from the dates, so a seven-month request
 * over one month of data still promises twenty thousand steps. Without this the
 * discrepancy only becomes visible after the run, in the shape of a result that
 * covered a fraction of what was asked for.
 */
function CoverageNotice({
  coverage,
  from,
  timeframe,
}: {
  coverage: CandleCoverage;
  from: string;
  timeframe: string;
}) {
  const { t } = useTranslation();
  if (coverage.candles === 0 || !coverage.from) {
    return (
      <div className="banner banner--error">
        {t('backtest.coverageEmpty', { timeframe })}
      </div>
    );
  }

  const stored = new Date(coverage.from);
  const requested = new Date(`${from}T00:00:00Z`);
  // A day of slack: a range starting on the same day as the first candle is not
  // a mismatch worth a warning.
  const short = stored.getTime() - requested.getTime() > 24 * 60 * 60 * 1000;
  if (!short) {
    return (
      <div className="banner">
        {t('backtest.coverageOk', {
          timeframe,
          candles: coverage.candles,
          from: formatDateTime(coverage.from),
        })}
      </div>
    );
  }
  return (
    <div className="banner banner--warn">
      <strong>{t('backtest.coverageShortTitle')}</strong>
      <p className="short">
        {t('backtest.coverageShort', {
          timeframe,
          stored: formatDateTime(coverage.from),
          requested: formatDateTime(requested.toISOString()),
        })}
      </p>
    </div>
  );
}

/**
 * DecisionReasons explains a run that produced few or no trades.
 *
 * A backtest that ends with zero trades is otherwise indistinguishable from a
 * broken one. The engine counts why every analysis point was refused, and the
 * counts answer the only question worth asking at that point: which rule said
 * no, and how often.
 */
function DecisionReasons({ metrics, run }: { metrics?: BacktestMetrics; run: BacktestRun }) {
  const { t } = useTranslation();
  const reasons = metrics?.decision_reasons;
  if (!reasons || Object.keys(reasons).length === 0) return null;

  const ordered = Object.entries(reasons).sort((a, b) => b[1] - a[1]);
  const total = ordered.reduce((sum, [, count]) => sum + count, 0);
  const noTrades = (metrics?.trades ?? 0) === 0;
  const replayed =
    metrics?.replay_from && metrics?.replay_to
      ? `${formatDateTime(metrics.replay_from)} — ${formatDateTime(metrics.replay_to)}`
      : null;
  const requested = `${formatDateTime(run.date_from)} — ${formatDateTime(run.date_to)}`;

  return (
    <div className={`banner ${noTrades ? 'banner--warn' : ''}`} style={{ marginTop: 12 }}>
      <strong>{noTrades ? t('backtest.noTradesTitle') : t('backtest.decisionsTitle')}</strong>
      {noTrades && <p className="short">{t('backtest.noTradesHint')}</p>}
      {replayed && (
        <p className="faint short">
          {t('backtest.replaySpan', { replayed, requested })}
        </p>
      )}
      <div className="table-wrap" style={{ marginTop: 8 }}>
        <table>
          <thead>
            <tr>
              <th>{t('backtest.decisionReason')}</th>
              <th>{t('backtest.decisionCount')}</th>
              <th>{t('backtest.decisionShare')}</th>
            </tr>
          </thead>
          <tbody>
            {ordered.map(([reason, count]) => (
              <tr key={reason}>
                <td>{t(`backtest.reasons.${reason}`, { defaultValue: reason })}</td>
                <td className="mono">{count}</td>
                <td className="mono">{total > 0 ? `${formatNumber((count / total) * 100)}%` : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!!metrics?.unfilled_entries && (
        <p className="faint short">
          {t('backtest.unfilledEntries', { count: metrics.unfilled_entries })}
        </p>
      )}
    </div>
  );
}

/**
 * RunId shows the identifier of a backtest and copies it on click.
 *
 * A run is only worth discussing if it can be named. The table shows a short
 * prefix so the column stays narrow, the open run shows the whole thing, and
 * either can be copied with one click - which is the only way an identifier of
 * this shape is ever going to leave the screen correctly.
 */
function RunId({ id, full = false }: { id: string; full?: boolean }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(id);
    } catch {
      // A denied clipboard is not worth an error banner: the identifier is
      // selectable on screen either way.
      return;
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };

  return (
    <button
      type="button"
      className={`run-id${full ? ' run-id--full' : ''}`}
      onClick={copy}
      title={full ? t('backtest.copyId') : id}
    >
      <span className="mono">{full ? id : id.slice(0, 8)}</span>
      <span className="faint">{copied ? t('backtest.idCopied') : t('backtest.copyId')}</span>
    </button>
  );
}

/**
 * RunFilters narrows the list of runs. The same values drive the bulk hide, so
 * "hide what I am looking at" always means exactly what the table shows.
 */
function RunFilters({
  filter,
  onChange,
  symbols,
}: {
  filter: BacktestFilter;
  onChange: (next: BacktestFilter) => void;
  symbols: string[];
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<BacktestFilter>) => onChange({ ...filter, ...patch });
  const active = Object.values(filter).some((value) => value);

  return (
    <div className="filters">
      <label className="field field--compact">
        <span className="field__label">{t('backtest.mode')}</span>
        <select value={filter.mode ?? ''} onChange={(e) => set({ mode: e.target.value })}>
          <option value="">{t('backtest.filterAny')}</option>
          <option value="technical">{t('backtest.technical')}</option>
          <option value="llm">{t('backtest.llm')}</option>
        </select>
      </label>
      <label className="field field--compact">
        <span className="field__label">{t('backtest.symbol')}</span>
        <select value={filter.symbol ?? ''} onChange={(e) => set({ symbol: e.target.value })}>
          <option value="">{t('backtest.filterAny')}</option>
          {symbols.map((symbol) => (
            <option key={symbol} value={symbol}>
              {symbol}
            </option>
          ))}
        </select>
      </label>
      <label className="field field--compact">
        <span className="field__label">{t('backtest.timeframe')}</span>
        <select value={filter.timeframe ?? ''} onChange={(e) => set({ timeframe: e.target.value })}>
          <option value="">{t('backtest.filterAny')}</option>
          {['1m', '5m', '15m', '1h', '4h', '1d'].map((tf) => (
            <option key={tf} value={tf}>
              {tf}
            </option>
          ))}
        </select>
      </label>
      <label className="field field--compact">
        <span className="field__label">{t('backtest.status')}</span>
        <select value={filter.status ?? ''} onChange={(e) => set({ status: e.target.value })}>
          <option value="">{t('backtest.filterAny')}</option>
          {['completed', 'running', 'pending', 'failed', 'canceled'].map((status) => (
            <option key={status} value={status}>
              {t(`enums.backtestStatus.${status}`, { defaultValue: status })}
            </option>
          ))}
        </select>
      </label>
      {active && (
        <button className="small ghost" onClick={() => onChange({})}>
          {t('backtest.filterReset')}
        </button>
      )}
    </div>
  );
}

/**
 * HideRuns clears the list without losing anything: the rows are marked hidden
 * and keep their parameters, metrics and trades. Runs that are still working are
 * never touched - hiding something about to write its results would read as a
 * lost run rather than a tidy list.
 */
function HideRuns({
  filter,
  total,
  onHidden,
}: {
  filter: BacktestFilter;
  total: number;
  onHidden: () => void;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const filtered = Object.values(filter).some((value) => value);

  const hide = async () => {
    setBusy(true);
    try {
      await api.hideBacktests(filter);
      onHidden();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };

  if (total === 0) return null;
  if (!confirming) {
    return (
      <button className="small ghost" onClick={() => setConfirming(true)}>
        {filtered ? t('backtest.hideFiltered', { count: total }) : t('backtest.hideAll', { count: total })}
      </button>
    );
  }
  return (
    <>
      <button className="small danger" disabled={busy} onClick={hide}>
        {t('backtest.hideConfirm')}
      </button>
      <button className="small ghost" disabled={busy} onClick={() => setConfirming(false)}>
        {t('app.cancel')}
      </button>
    </>
  );
}
