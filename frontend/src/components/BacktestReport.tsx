import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type { BacktestRun, BacktestTrade, EquityPoint } from '../api/types';
import { CandleChart, type ChartMarker } from '../charts/CandleChart';
import { EquityChart } from '../charts/EquityChart';
import { useApi } from '../hooks/useApi';
import { formatNumber, toNumber } from '../utils/format';
import { Card } from './common';

/** Breakdown draws a labelled bar per row, sized against the largest value. */
function Breakdown({
  items,
  empty,
}: {
  items: { key: string; label: string; value: number; caption?: string; tone?: 'long' | 'short' }[];
  empty: string;
}) {
  const max = items.reduce((acc, item) => Math.max(acc, item.value), 0);
  if (items.length === 0 || max === 0) return <p className="faint short">{empty}</p>;

  return (
    <div className="breakdown">
      {items.map((item) => (
        <div key={item.key} className="breakdown__row">
          <span className="breakdown__label">{item.label}</span>
          <span className="breakdown__track">
            <span
              className={`breakdown__bar${item.tone ? ` breakdown__bar--${item.tone}` : ''}`}
              style={{ width: `${Math.max(2, (item.value / max) * 100)}%` }}
            />
          </span>
          <span className="breakdown__value numeric">
            {item.value}
            {item.caption ? <span className="faint"> · {item.caption}</span> : null}
          </span>
        </div>
      ))}
    </div>
  );
}

/**
 * BacktestReport turns a finished run into something readable: the equity curve
 * with its drawdown, every fill drawn on the price the run replayed, and two
 * distributions that a single aggregate number hides - how trades ended and how
 * their results were spread.
 */
export function BacktestReport({
  run,
  trades,
  equityCurve,
}: {
  run: BacktestRun;
  trades: BacktestTrade[];
  equityCurve: EquityPoint[];
}) {
  const { t } = useTranslation();

  // Only the replayed window is loaded, so the chart shows the run and not the
  // most recent bars of an unrelated period.
  const candles = useApi(
    () => api.candles(run.symbol, run.timeframe, 5000, { from: run.date_from, to: run.date_to }),
    [run.id],
  );

  const markers = useMemo<ChartMarker[]>(() => {
    const out: ChartMarker[] = [];
    for (const trade of trades) {
      const long = trade.direction === 'LONG';
      for (const execution of trade.executions ?? []) {
        if (execution.kind === 'funding') continue;
        if (execution.kind === 'entry') {
          out.push({
            time: execution.executed_at,
            text: `${long ? 'LONG' : 'SHORT'} ${formatNumber(execution.price)}`,
            position: long ? 'belowBar' : 'aboveBar',
            color: long ? '#3fbf87' : '#f2617a',
            shape: long ? 'arrowUp' : 'arrowDown',
          });
          continue;
        }
        const profit = toNumber(execution.gross_pnl) ?? 0;
        out.push({
          time: execution.executed_at,
          text: `${t(`backtest.exitReasons.${execution.kind}`, { defaultValue: execution.kind })} ${Math.round(
            execution.close_pct ?? 100,
          )}%`,
          position: long ? 'aboveBar' : 'belowBar',
          color: profit >= 0 ? '#3fbf87' : '#f2617a',
          shape: execution.kind === 'liquidation' ? 'square' : 'circle',
        });
      }
    }
    return out;
  }, [trades, t]);

  const exitReasons = useMemo(() => {
    const counts = new Map<string, number>();
    for (const trade of trades) {
      for (const execution of trade.executions ?? []) {
        if (execution.kind === 'entry' || execution.kind === 'funding') continue;
        counts.set(execution.kind, (counts.get(execution.kind) ?? 0) + 1);
      }
    }
    return [...counts.entries()]
      .sort((a, b) => b[1] - a[1])
      .map(([kind, value]) => ({
        key: kind,
        label: t(`backtest.exitReasons.${kind}`, { defaultValue: kind }),
        value,
        tone: kind === 'take_profit' ? ('long' as const) : ('short' as const),
      }));
  }, [trades, t]);

  // Which strategies actually asked for the trades that were taken. Only the
  // deterministic mode records votes, so this stays empty for an LLM run.
  const strategyUsage = useMemo(() => {
    const totals = new Map<string, { count: number; weight: number }>();
    for (const trade of trades) {
      for (const vote of trade.strategy_votes ?? []) {
        const current = totals.get(vote.id) ?? { count: 0, weight: 0 };
        totals.set(vote.id, {
          count: current.count + 1,
          weight: current.weight + Math.abs(vote.score),
        });
      }
    }
    return [...totals.entries()]
      .sort((a, b) => b[1].count - a[1].count)
      .map(([id, value]) => ({
        key: id,
        label: t(`strategies.${id}`, { defaultValue: id }),
        value: value.count,
        caption: formatNumber(value.weight, 1),
      }));
  }, [trades, t]);

  const distribution = useMemo(() => {
    if (trades.length === 0) return [];
    const values = trades.map((trade) => trade.pnl_pct);
    const min = Math.min(...values);
    const max = Math.max(...values);
    if (!Number.isFinite(min) || !Number.isFinite(max)) return [];

    const buckets = 6;
    const width = (max - min) / buckets || 1;
    const counts = new Array<number>(buckets).fill(0);
    for (const value of values) {
      const index = Math.min(buckets - 1, Math.floor((value - min) / width));
      counts[index] += 1;
    }
    return counts.map((value, index) => {
      const from = min + width * index;
      const to = index === buckets - 1 ? max : from + width;
      return {
        key: `bucket-${index}`,
        label: `${formatNumber(from, 1)}…${formatNumber(to, 1)}%`,
        value,
        tone: to <= 0 ? ('short' as const) : from >= 0 ? ('long' as const) : undefined,
      };
    });
  }, [trades]);

  const initialCapital = toNumber(run.params?.initial_capital) ?? undefined;

  return (
    <>
      {equityCurve.length > 1 && (
        <Card title={t('backtest.equityCurve')}>
          <p className="faint short">{t('backtest.equityCurveHint')}</p>
          <EquityChart points={equityCurve} initialCapital={initialCapital} />
        </Card>
      )}

      <Card title={t('backtest.tradesOnChart')}>
        {candles.loading && <p className="faint">{t('app.loading')}</p>}
        {!candles.loading && !candles.data?.candles.length && <p className="faint">{t('backtest.noCandles')}</p>}
        {!!candles.data?.candles.length && (
          <>
            <p className="faint short">{t('backtest.tradesOnChartHint')}</p>
            <CandleChart candles={candles.data.candles} markers={markers} height={380} />
          </>
        )}
      </Card>

      {strategyUsage.length > 0 && (
        <Card title={t('strategyStats.title')}>
          <p className="faint short">{t('strategyStats.hint')}</p>
          <Breakdown items={strategyUsage} empty={t('backtest.noTrades')} />
        </Card>
      )}

      <Card title={t('backtest.distributions')}>
        <div className="grid grid--2">
          <div>
            <h3 className="short">{t('backtest.exitBreakdown')}</h3>
            <Breakdown items={exitReasons} empty={t('backtest.noTrades')} />
          </div>
          <div>
            <h3 className="short">{t('backtest.pnlDistribution')}</h3>
            <Breakdown items={distribution} empty={t('backtest.noTrades')} />
          </div>
        </div>
      </Card>
    </>
  );
}
