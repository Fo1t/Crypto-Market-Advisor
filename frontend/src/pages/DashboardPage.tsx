import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Badge, Card, Stat } from '../components/common';
import { RecommendationCard } from '../components/RecommendationCard';
import { PositionCard } from '../components/PositionCard';
import { formatAge, formatMoney, formatNumber, formatPct, formatPrice, toneOf } from '../utils/format';

export function DashboardPage() {
  const { t } = useTranslation();
  const { data, loading, error, reload } = useApi(() => api.dashboard(), [], 30_000);

  return (
    <AsyncBoundary loading={loading} error={error} onRetry={reload} hasData={!!data}>
      {data && (
        <>
          {!data.fees_configured && <div className="banner banner--warn">{t('status.feesNotConfigured')}</div>}

          <Card title={t('dashboard.marketOverview')}>
            <div className="grid grid--3">
              {data.markets.map((market) => (
                <Link key={market.id} to={`/markets/${market.symbol}`} className="stat" style={{ gap: 6 }}>
                  <div className="row row--between">
                    <span style={{ fontWeight: 600 }}>{market.symbol}</span>
                    <span className={`mono ${toneOf(market.price_change_24h_pct) ?? ''}`}>
                      {formatPct(market.price_change_24h_pct)}
                    </span>
                  </div>
                  <span className="stat__value">{formatPrice(market.price)}</span>
                  <div className="row" style={{ gap: 6 }}>
                    {market.market_regime && <Badge>{t(`enums.regime.${market.market_regime}`)}</Badge>}
                    {market.last_action && (
                      <Badge
                        tone={
                          market.last_action === 'OPEN_LONG'
                            ? 'long'
                            : market.last_action === 'OPEN_SHORT'
                              ? 'short'
                              : undefined
                        }
                      >
                        {t(`enums.action.${market.last_action}`)} {market.last_confidence ?? ''}
                      </Badge>
                    )}
                  </div>
                  <span className="stat__hint">
                    {market.rsi !== undefined ? `RSI ${formatNumber(market.rsi, 1)} · ` : ''}
                    {market.last_signal_at ? formatAge(market.last_signal_at) : t('app.never')}
                  </span>
                </Link>
              ))}
              {data.markets.length === 0 && <span className="faint">{t('app.empty')}</span>}
            </div>
          </Card>

          {data.performance && (
            <Card title={t('dashboard.performance')}>
              <div className="grid grid--4">
                <Stat label={t('statistics.winRate')} value={`${(data.performance.win_rate * 100).toFixed(0)}%`} />
                <Stat
                  label={t('statistics.profitFactor')}
                  value={data.performance.profit_factor ? formatNumber(data.performance.profit_factor) : '—'}
                />
                <Stat
                  label={t('statistics.realizedPnl')}
                  value={formatMoney(data.performance.realized_pnl)}
                  tone={toneOf(data.performance.realized_pnl)}
                />
                <Stat label={t('statistics.predictions')} value={data.performance.predictions} />
              </div>
            </Card>
          )}

          <Card title={t('dashboard.openPositions')}>
            <div className="stack">
              {data.open_positions.map((view) => (
                <PositionCard key={view.position.id} view={view} onChanged={reload} />
              ))}
              {data.open_positions.length === 0 && <span className="faint">{t('dashboard.noPositions')}</span>}
            </div>
          </Card>

          <Card title={t('dashboard.recentRecommendations')}>
            <div className="stack">
              {data.recent_recommendations.map((rec) => (
                <RecommendationCard key={rec.id} recommendation={rec} onChanged={reload} compact />
              ))}
              {data.recent_recommendations.length === 0 && (
                <span className="faint">{t('dashboard.noRecommendations')}</span>
              )}
            </div>
          </Card>
        </>
      )}
    </AsyncBoundary>
  );
}
