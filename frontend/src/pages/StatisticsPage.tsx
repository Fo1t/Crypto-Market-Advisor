import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type { StatBucket } from '../api/types';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Card, Stat } from '../components/common';
import { formatMinutes, formatMoney, formatNumber, toneOf } from '../utils/format';

export function StatisticsPage() {
  const { t } = useTranslation();
  const [days, setDays] = useState<number | undefined>(30);
  const { data, loading, error, reload } = useApi(() => api.statistics(days), [days]);

  return (
    <AsyncBoundary loading={loading} error={error} onRetry={reload} hasData={!!data}>
      {data && (
        <>
          <Card
            title={t('statistics.title')}
            actions={
              <div className="tabs">
                {[
                  { value: 7, label: t('statistics.window7d') },
                  { value: 30, label: t('statistics.window30d') },
                  { value: 90, label: t('statistics.window90d') },
                  { value: 0, label: t('statistics.windowAll') },
                ].map((option) => (
                  <button
                    key={option.value}
                    className={(option.value === 0 ? undefined : option.value) === days ? 'tab tab--active' : 'tab'}
                    onClick={() => setDays(option.value === 0 ? undefined : option.value)}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
            }
          >
            <div className="grid grid--4">
              <Stat label={t('statistics.predictions')} value={data.predictions} />
              <Stat label={t('statistics.positionsOpened')} value={data.positions_opened} />
              <Stat label={t('statistics.positionsClosed')} value={data.positions_closed} />
              <Stat label={t('statistics.outcomesResolved')} value={data.outcomes_resolved} />
              <Stat label={t('statistics.ambiguous')} value={data.ambiguous_outcomes} />
              <Stat label={t('statistics.winRate')} value={`${(data.win_rate * 100).toFixed(0)}%`} />
              <Stat
                label={t('statistics.profitFactor')}
                value={data.profit_factor ? formatNumber(data.profit_factor) : '—'}
              />
              <Stat
                label={t('statistics.realizedPnl')}
                value={formatMoney(data.realized_pnl)}
                tone={toneOf(data.realized_pnl)}
              />
              <Stat label={t('statistics.avgPnl')} value={formatMoney(data.average_pnl)} tone={toneOf(data.average_pnl)} />
              <Stat label={t('statistics.medianPnl')} value={formatMoney(data.median_pnl)} tone={toneOf(data.median_pnl)} />
              <Stat label={t('statistics.maxDrawdown')} value={formatMoney(data.max_drawdown)} />
              <Stat label={t('statistics.avgHolding')} value={formatMinutes(data.average_holding_minutes)} />
              <Stat label={t('statistics.avgMfe')} value={`${formatNumber(data.average_mfe_pct)}%`} tone="long" />
              <Stat label={t('statistics.avgMae')} value={`${formatNumber(data.average_mae_pct)}%`} tone="short" />
            </div>

            <div className="grid grid--4" style={{ marginTop: 14 }}>
              {Object.entries(data.action_counts).map(([action, count]) => (
                <Stat key={action} label={t(`enums.action.${action}`)} value={count} />
              ))}
            </div>
          </Card>

          <Card title={t('statistics.calibration')}>
            <p className="faint">{t('statistics.calibrationHint')}</p>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{t('statistics.byConfidence')}</th>
                    <th className="numeric">{t('statistics.count')}</th>
                    <th className="numeric">{t('statistics.actualRate')}</th>
                    <th className="numeric">{t('statistics.expectedRate')}</th>
                    <th className="numeric">{t('statistics.calibrationGap')}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.calibration.map((bucket) => (
                    <tr key={bucket.key}>
                      <td>{bucket.key}</td>
                      <td className="numeric">{bucket.count}</td>
                      <td className="numeric">{(bucket.win_rate * 100).toFixed(0)}%</td>
                      <td className="numeric">
                        {bucket.expected_rate !== undefined ? `${(bucket.expected_rate * 100).toFixed(0)}%` : '—'}
                      </td>
                      <td className={`numeric ${toneOf(bucket.calibration_gap) ?? ''}`}>
                        {bucket.calibration_gap !== undefined
                          ? `${(bucket.calibration_gap * 100).toFixed(0)}%`
                          : '—'}
                      </td>
                    </tr>
                  ))}
                  {data.calibration.length === 0 && (
                    <tr>
                      <td colSpan={5} className="faint">
                        {t('app.empty')}
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </Card>

          <div className="grid grid--2">
            <BucketTable title={t('statistics.bySymbol')} buckets={data.by_symbol} />
            <BucketTable title={t('statistics.byDirection')} buckets={data.by_direction} />
            <BucketTable title={t('statistics.byRegime')} buckets={data.by_regime} />
            <BucketTable title={t('statistics.byLeverage')} buckets={data.by_leverage} />
          </div>
        </>
      )}
    </AsyncBoundary>
  );
}

function BucketTable({ title, buckets }: { title: string; buckets: StatBucket[] }) {
  const { t } = useTranslation();
  return (
    <Card title={title} inset>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{title}</th>
              <th className="numeric">{t('statistics.count')}</th>
              <th className="numeric">{t('statistics.winRate')}</th>
              <th className="numeric">{t('statistics.avgPnl')}</th>
            </tr>
          </thead>
          <tbody>
            {buckets.map((bucket) => (
              <tr key={bucket.key}>
                <td>{bucket.key}</td>
                <td className="numeric">{bucket.count}</td>
                <td className="numeric">{(bucket.win_rate * 100).toFixed(0)}%</td>
                <td className={`numeric ${toneOf(bucket.average_pnl) ?? ''}`}>
                  {formatMoney(bucket.average_pnl)}
                </td>
              </tr>
            ))}
            {buckets.length === 0 && (
              <tr>
                <td colSpan={4} className="faint">
                  {t('app.empty')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
