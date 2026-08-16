import { useTranslation } from 'react-i18next';
import { useState } from 'react';

import { api } from '../api/client';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Badge, Card } from '../components/common';
import { formatDateTime, formatMoney, formatNumber, formatPrice, toneOf } from '../utils/format';

/**
 * HistoryPage lists past predictions next to what the market actually did and
 * what the user decided. The three are shown side by side on purpose: a
 * prediction is never rewritten once its outcome is known.
 */
export function HistoryPage() {
  const { t, i18n } = useTranslation();
  const [days, setDays] = useState<number | undefined>(30);

  const recommendations = useApi(() => api.recommendations({ limit: 100, days }), [days]);
  const positions = useApi(() => api.positions(false), []);

  return (
    <>
      <Card
        title={t('nav.history')}
        actions={
          <div className="tabs">
            {[7, 30, 90, 0].map((value) => (
              <button
                key={value}
                className={(value === 0 ? undefined : value) === days ? 'tab tab--active' : 'tab'}
                onClick={() => setDays(value === 0 ? undefined : value)}
              >
                {value === 0 ? t('statistics.windowAll') : `${value}d`}
              </button>
            ))}
          </div>
        }
      >
        <AsyncBoundary
          loading={recommendations.loading}
          error={recommendations.error}
          onRetry={recommendations.reload}
          hasData={!!recommendations.data}
        >
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('app.edit')}</th>
                  <th>{t('markets.symbol')}</th>
                  <th>{t('recommendation.action')}</th>
                  <th className="numeric">{t('recommendation.confidence')}</th>
                  <th className="numeric">{t('recommendation.leverage')}</th>
                  <th>{t('enums.decision.PENDING')}</th>
                  <th>{t('statistics.outcomesResolved')}</th>
                  <th className="numeric">MFE</th>
                  <th className="numeric">MAE</th>
                </tr>
              </thead>
              <tbody>
                {recommendations.data?.items.map((rec) => (
                  <tr key={rec.id}>
                    <td className="faint">{formatDateTime(rec.created_at, i18n.language)}</td>
                    <td>{rec.symbol}</td>
                    <td>
                      <Badge
                        tone={
                          rec.action === 'OPEN_LONG' ? 'long' : rec.action === 'OPEN_SHORT' ? 'short' : undefined
                        }
                      >
                        {t(`enums.action.${rec.action}`)}
                      </Badge>
                    </td>
                    <td className="numeric">{rec.confidence}%</td>
                    <td className="numeric">
                      {rec.leverage.recommended}x
                      {rec.leverage.llm_suggested !== rec.leverage.recommended && (
                        <span className="faint"> ← {rec.leverage.llm_suggested}x</span>
                      )}
                    </td>
                    <td>{rec.decision ? t(`enums.decision.${rec.decision.decision}`) : '—'}</td>
                    <td>
                      {rec.outcome ? (
                        <Badge
                          tone={
                            rec.outcome.result === 'win' ? 'long' : rec.outcome.result === 'loss' ? 'short' : 'warn'
                          }
                        >
                          {t(`enums.outcome.${rec.outcome.status}`)}
                        </Badge>
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                    <td className="numeric long">{formatNumber(rec.outcome?.max_favorable_excursion_pct, 2)}</td>
                    <td className="numeric short">{formatNumber(rec.outcome?.max_adverse_excursion_pct, 2)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </AsyncBoundary>
      </Card>

      <Card title={t('positions.closed')}>
        <AsyncBoundary
          loading={positions.loading}
          error={positions.error}
          onRetry={positions.reload}
          hasData={!!positions.data}
        >
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('markets.symbol')}</th>
                  <th>{t('positions.direction')}</th>
                  <th className="numeric">{t('positions.entry')}</th>
                  <th className="numeric">{t('positions.realized')}</th>
                  <th className="numeric">{t('positions.fees')}</th>
                  <th className="numeric">{t('positions.funding')}</th>
                  <th>{t('positions.result')}</th>
                  <th>{t('positions.openedAt')}</th>
                </tr>
              </thead>
              <tbody>
                {positions.data?.items
                  .filter((view) => view.position.status === 'CLOSED')
                  .map((view) => (
                    <tr key={view.position.id}>
                      <td>{view.position.symbol}</td>
                      <td className={view.position.direction === 'LONG' ? 'long' : 'short'}>
                        {t(`enums.direction.${view.position.direction}`)}
                      </td>
                      <td className="numeric">{formatPrice(view.position.entry_price)}</td>
                      <td className={`numeric ${toneOf(view.pnl.net_realized_pnl) ?? ''}`}>
                        {formatMoney(view.pnl.net_realized_pnl)}
                      </td>
                      <td className="numeric">{formatMoney(view.pnl.fees)}</td>
                      <td className="numeric">{formatMoney(view.pnl.funding)}</td>
                      <td>{t(`enums.result.${view.result}`)}</td>
                      <td className="faint">{formatDateTime(view.position.opened_at, i18n.language)}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        </AsyncBoundary>
      </Card>
    </>
  );
}
