import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';

import { api } from '../api/client';
import type { Timeframe } from '../api/types';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Badge, Bar, Card } from '../components/common';
import { OpenPositionModal } from '../components/OpenPositionModal';
import { StrategyDecisionCard } from '../components/StrategyDecisionCard';
import { CandleChart } from '../charts/CandleChart';
import { NewsEventCard } from '../components/NewsEventCard';
import { formatDateTime, formatNumber, formatPrice } from '../utils/format';

const TIMEFRAMES: Timeframe[] = ['1m', '5m', '15m', '1h', '4h', '1d'];
const VOLUME_NOTE = 'per-candle volume is unavailable from CoinGecko; only rolling 24h volume is known';
const STALE_OVERVIEW_NOTE = 'market overview is older than 15 minutes';
const CONFLICT_RE = /^(\S+) (bullish|bearish) vs (\S+) (bullish|bearish)$/;

function translateMissingField(field: string, t: TFunction): string {
  if (field.startsWith('timeframe_')) {
    return t('market.missingTimeframe', { timeframe: field.slice('timeframe_'.length) });
  }
  return t(`enums.missingField.${field}`, { defaultValue: field });
}

function translateDataNote(note: string, t: TFunction): string {
  if (note === VOLUME_NOTE) return t('market.dataNoteVolume');
  if (note === STALE_OVERVIEW_NOTE) return t('market.dataNoteStaleOverview');
  return note;
}

function translateConflict(conflict: string, t: TFunction): string {
  const match = conflict.match(CONFLICT_RE);
  if (!match) return conflict;
  return t('market.timeframeConflict', {
    fastTf: match[1],
    fastBias: t(`enums.bias.${match[2]}`),
    slowTf: match[3],
    slowBias: t(`enums.bias.${match[4]}`),
  });
}

function translateStructure(description: string | undefined, state: string, t: TFunction): string {
  if (!description) return '—';
  if (description === 'insufficient history') return t('analysis.structureDescription.insufficientHistory');
  if (description === 'no confirmed swing points') return t('analysis.structureDescription.noConfirmedSwings');
  if (!description.startsWith(`${state} `)) return description;
  return description.slice(state.length + 1);
}

function humanizeIdentifier(value: string): string {
  return value
    .split('_')
    .map((part) => part.toUpperCase() === part ? part : part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ');
}

function translateIndicatorValue(value: string, t: TFunction): string {
  return t(`analysis.indicatorState.${value}`, { defaultValue: humanizeIdentifier(value) });
}

export function MarketDetailPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const { symbol = '' } = useParams();
  const [timeframe, setTimeframe] = useState<Timeframe>('1h');
  const [showCreatePosition, setShowCreatePosition] = useState(false);

  const candles = useApi(() => api.candles(symbol, timeframe), [symbol, timeframe], 60_000);
  const analysis = useApi(() => api.analysis(symbol), [symbol], 60_000);
  const relatedNews = useApi(() => api.news({ asset: symbol, days: 7, limit: 6, sort: 'importance' }), [symbol], 60_000);

  const snapshot = analysis.data?.features_snapshot;
  const tfAnalysis = snapshot?.timeframes?.[timeframe];
  const candleList = candles.data?.candles ?? [];
  const latestCandlePrice = candleList.length > 0 ? candleList[candleList.length - 1].close : undefined;
  const currentPrice = snapshot?.price ?? latestCandlePrice;

  return (
    <>
      <Card
        title={`${symbol} · ${t('market.chart')}`}
        actions={
          <div className="row">
            <div className="tabs">
              {TIMEFRAMES.map((tf) => (
                <button
                  key={tf}
                  className={tf === timeframe ? 'tab tab--active' : 'tab'}
                  onClick={() => setTimeframe(tf)}
                >
                  {tf}
                </button>
              ))}
            </div>
            <button className="small primary" onClick={() => setShowCreatePosition(true)}>
              {t('recommendation.opened')}
            </button>
          </div>
        }
      >
        <AsyncBoundary
          loading={candles.loading}
          error={candles.error}
          onRetry={candles.reload}
          hasData={!!candles.data}
        >
          <CandleChart
            candles={candles.data?.candles ?? []}
            levels={(tfAnalysis?.levels ?? snapshot?.support_resistance ?? []).slice(0, 8)}
            newsMarkers={(relatedNews.data?.items ?? []).map((event) => ({
              time: event.first_published_at,
              title: event.canonical_title,
              critical: event.critical,
            }))}
          />
          <div className="row faint" style={{ marginTop: 8 }}>
            <span>
              {t('market.lastClosedCandle')}:{' '}
              {formatDateTime(snapshot?.latest_closed_candle_timestamp, i18n.language)}
            </span>
            {tfAnalysis?.candle_source_mix && (
              <span>
                {t('market.candleSource')}:{' '}
                {Object.entries(tfAnalysis.candle_source_mix)
                  .map(([source, count]) => `${t(`enums.candleSource.${source}`)} ${count}`)
                  .join(' · ')}
              </span>
            )}
            {tfAnalysis?.candle_provider_mix && (
              <span>
                {t('market.marketProvider')}:{' '}
                {Object.entries(tfAnalysis.candle_provider_mix).map(([provider, count]) => `${provider === 'bybit' ? 'Bybit' : 'CoinGecko'} ${count}`).join(' · ')}
              </span>
            )}
          </div>
        </AsyncBoundary>
      </Card>

      <AsyncBoundary
        loading={analysis.loading}
        error={analysis.error}
        onRetry={analysis.reload}
        hasData={!!analysis.data}
      >
        {!snapshot && <span className="faint">{t('market.noAnalysis')}</span>}

        {snapshot && (
          <>
            {snapshot.data_quality.status !== 'ok' && (
              <div className="banner banner--warn">
                {t('market.dataQuality')}: {t(`enums.dataQuality.${snapshot.data_quality.status}`)} ·{' '}
                {t('market.missingFields')}:{' '}
                {snapshot.data_quality.missing_fields.map((field) => translateMissingField(field, t)).join(', ') || '—'}
                {snapshot.data_quality.notes?.length
                  ? ` · ${snapshot.data_quality.notes.map((note) => translateDataNote(note, t)).join('; ')}`
                  : ''}
              </div>
            )}

            <div className="grid grid--2">
              <Card title={t('market.regime')} inset>
                <div className="stack">
                  <div className="row">
                    <Badge tone="accent">{t(`enums.regime.${snapshot.market_regime.primary}`)}</Badge>
                    {snapshot.market_regime.tags?.map((tag) => (
                      <Badge key={tag}>{t(`enums.regimeTag.${tag}`)}</Badge>
                    ))}
                  </div>
                  <Bar
                    label={t('market.alignment')}
                    value={snapshot.trend_alignment.alignment_score}
                    tone={snapshot.trend_alignment.alignment_score >= 0 ? 'long' : 'short'}
                    display={formatNumber(snapshot.trend_alignment.alignment_score, 2)}
                  />
                  <div className="faint">
                    ↑ {snapshot.trend_alignment.bullish.join(', ') || '—'} · ↓{' '}
                    {snapshot.trend_alignment.bearish.join(', ') || '—'} · ∅{' '}
                    {snapshot.trend_alignment.neutral.join(', ') || '—'}
                  </div>
                  {snapshot.trend_alignment.conflicts?.map((conflict) => (
                    <div key={conflict} className="faint warn">
                      {translateConflict(conflict, t)}
                    </div>
                  ))}
                </div>
              </Card>

              {analysis.data?.strategy_decision && (
                <StrategyDecisionCard decision={analysis.data.strategy_decision} inset />
              )}

              <Card title={t('market.scores')} inset>
                <div className="stack">
                  <Bar
                    label={t('market.scores')}
                    value={snapshot.signal_scores.net_score}
                    tone={snapshot.signal_scores.net_score >= 0 ? 'long' : 'short'}
                    display={formatNumber(snapshot.signal_scores.net_score, 2)}
                  />
                  <Bar label={t('market.scoreTrend')} value={snapshot.signal_scores.trend_score} display={formatNumber(snapshot.signal_scores.trend_score, 2)} />
                  <Bar label={t('market.scoreMomentum')} value={snapshot.signal_scores.momentum_score} display={formatNumber(snapshot.signal_scores.momentum_score, 2)} />
                  <Bar label={t('market.scorePattern')} value={snapshot.signal_scores.pattern_score} display={formatNumber(snapshot.signal_scores.pattern_score, 2)} />
                  <Bar
                    label={t('market.scoreVolatilityRisk')}
                    value={snapshot.signal_scores.volatility_risk_score}
                    tone="short"
                    display={formatNumber(snapshot.signal_scores.volatility_risk_score, 2)}
                  />
                </div>
              </Card>
            </div>

            {tfAnalysis && (
              <div className="grid grid--2">
                <Card title={`${t('market.indicators')} · ${timeframe}`} inset>
                  <div className="table-wrap">
                    <table>
                      <tbody>
                        {Object.entries(tfAnalysis.indicators)
                          .filter(([, value]) => typeof value === 'number' || typeof value === 'string')
                          .map(([key, value]) => (
                            <tr key={key}>
                              <td className="muted">
                                {t(`analysis.indicator.${key}`, { defaultValue: humanizeIdentifier(key) })}
                              </td>
                              <td className="numeric">
                                {typeof value === 'number' ? formatNumber(value, 4) : translateIndicatorValue(String(value), t)}
                              </td>
                            </tr>
                          ))}
                      </tbody>
                    </table>
                  </div>
                </Card>

                <div className="stack">
                  <Card title={`${t('market.structure')} · ${timeframe}`} inset>
                    <div className="row">
                      <Badge tone="accent">{t(`enums.structure.${tfAnalysis.structure.state}`)}</Badge>
                      <span className="faint">
                        {translateStructure(tfAnalysis.structure.description, tfAnalysis.structure.state, t)}
                      </span>
                    </div>
                  </Card>

                  <Card title={t('market.patterns')} inset>
                    <div className="row">
                      {(tfAnalysis.patterns ?? []).slice(0, 8).map((pattern, i) => (
                        <Badge
                          key={`${pattern.name}-${i}`}
                          tone={pattern.direction === 'bullish' ? 'long' : pattern.direction === 'bearish' ? 'short' : undefined}
                        >
                          {t(`analysis.pattern.${pattern.name}`, { defaultValue: humanizeIdentifier(pattern.name) })} ·{' '}
                          {formatNumber(pattern.strength, 2)}
                        </Badge>
                      ))}
                      {(tfAnalysis.patterns ?? []).length === 0 && <span className="faint">{t('app.empty')}</span>}
                    </div>
                  </Card>

                  <Card title={t('market.chartPatterns')} inset>
                    <div className="row">
                      {(tfAnalysis.chart_patterns ?? []).map((pattern, i) => (
                        <Badge
                          key={`${pattern.name}-${i}`}
                          tone={pattern.direction === 'bullish' ? 'long' : pattern.direction === 'bearish' ? 'short' : undefined}
                        >
                          {t(`analysis.pattern.${pattern.name}`, { defaultValue: humanizeIdentifier(pattern.name) })} ·{' '}
                          {formatNumber(pattern.strength, 2)}
                        </Badge>
                      ))}
                      {(tfAnalysis.chart_patterns ?? []).length === 0 && (
                        <span className="faint">{t('app.empty')}</span>
                      )}
                    </div>
                  </Card>

                  <Card title={t('market.divergences')} inset>
                    <div className="row">
                      {(tfAnalysis.divergences ?? []).map((divergence, i) => (
                        <Badge
                          key={i}
                          tone={divergence.direction === 'bullish' ? 'long' : 'short'}
                        >
                          {divergence.indicator.toUpperCase()} · {t(`analysis.divergenceType.${divergence.type}`)} ·{' '}
                          {t(`enums.bias.${divergence.direction}`)} · {formatNumber(divergence.strength, 2)}
                        </Badge>
                      ))}
                      {(tfAnalysis.divergences ?? []).length === 0 && (
                        <span className="faint">{t('app.empty')}</span>
                      )}
                    </div>
                  </Card>
                </div>
              </div>
            )}

            <Card title={t('market.levels')}>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th className="numeric">{t('markets.price')}</th>
                      <th>{t('app.edit')}</th>
                      <th className="numeric">{t('market.strength')}</th>
                      <th className="numeric">{t('market.touches')}</th>
                      <th className="numeric">{t('market.distance')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {snapshot.support_resistance.map((level, i) => (
                      <tr key={i}>
                        <td className="numeric">{formatPrice(level.price)}</td>
                        <td className={level.type === 'support' ? 'long' : 'short'}>
                          {t(`enums.levelType.${level.type}`)}
                        </td>
                        <td className="numeric">{formatNumber(level.strength, 2)}</td>
                        <td className="numeric">{level.touches}</td>
                        <td className="numeric">{formatNumber(level.distance_pct, 2)}%</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </Card>

            <Card title={t('market.relevantNews')} actions={<Link className="button" to={`/news?asset=${encodeURIComponent(symbol)}`}>{t('market.allAssetNews')}</Link>}>
              {snapshot.news_context && snapshot.news_context.status !== 'ok' && snapshot.news_context.status !== 'available_but_empty' && (
                <div className="banner banner--warn">{t(`news.contextStatuses.${snapshot.news_context.status}`)}</div>
              )}
              <AsyncBoundary loading={relatedNews.loading} error={relatedNews.error} onRetry={relatedNews.reload} hasData={!!relatedNews.data}>
                <div className="stack">
                  {relatedNews.data?.items.map((event) => <NewsEventCard key={event.id} event={event} compact />)}
                  {relatedNews.data?.items.length === 0 && <span className="faint">{t('news.emptyForAsset')}</span>}
                </div>
              </AsyncBoundary>
            </Card>
          </>
        )}
      </AsyncBoundary>

      {showCreatePosition && (
        <OpenPositionModal
          symbol={symbol}
          entryPrice={currentPrice}
          onClose={() => setShowCreatePosition(false)}
          onCreated={() => {
            setShowCreatePosition(false);
            navigate('/positions');
          }}
        />
      )}
    </>
  );
}
