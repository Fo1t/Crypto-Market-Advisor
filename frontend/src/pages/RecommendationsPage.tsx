import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Card } from '../components/common';
import { RecommendationCard } from '../components/RecommendationCard';

const ACTIONS = ['', 'OPEN_LONG', 'OPEN_SHORT', 'NO_ACTION', 'MANAGE_POSITION'];
const RISKS = ['', 'low', 'medium', 'high', 'extreme'];
const DATA_QUALITIES = ['', 'ok', 'degraded', 'unusable'];

export function RecommendationsPage() {
  const { t } = useTranslation();
  const [action, setAction] = useState('');
  const [symbol, setSymbol] = useState('');
  const [riskLevel, setRiskLevel] = useState('');
  const [minConfidence, setMinConfidence] = useState('');
  const [maxConfidence, setMaxConfidence] = useState('');
  const [dataQuality, setDataQuality] = useState('');
  const [visibility, setVisibility] = useState('active');
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkMessage, setBulkMessage] = useState<string | null>(null);

  const { data, loading, error, reload } = useApi(
    () =>
      api.recommendations({
        action: action || undefined,
        symbol: symbol || undefined,
        risk_level: riskLevel || undefined,
        min_confidence: minConfidence === '' ? undefined : Number(minConfidence),
        max_confidence: maxConfidence === '' ? undefined : Number(maxConfidence),
        data_quality: dataQuality || undefined,
        visibility,
        limit: 30,
      }),
    [action, symbol, riskLevel, minConfidence, maxConfidence, dataQuality, visibility],
    60_000,
  );

  const dismissAll = async () => {
    if (!window.confirm(t('recommendation.dismissAllConfirm'))) return;
    setBulkBusy(true);
    setBulkMessage(null);
    try {
      const result = await api.dismissAllRecommendations();
      setBulkMessage(t('recommendation.dismissedAll', { count: result.dismissed_count }));
      reload();
    } catch (err) {
      setBulkMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBulkBusy(false);
    }
  };

  return (
    <Card
      title={t('nav.recommendations')}
      actions={
        <div className="recommendation-filters">
          <label className="field">
            <span className="field__label">{t('recommendation.filterAction')}</span>
            <select value={action} onChange={(e) => setAction(e.target.value)}>
              {ACTIONS.map((value) => (
                <option key={value} value={value}>
                  {value ? t(`enums.action.${value}`) : t('app.all')}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span className="field__label">{t('recommendation.filterSymbol')}</span>
            <input value={symbol} onChange={(e) => setSymbol(e.target.value.toUpperCase())} placeholder="BTC" />
          </label>
          <label className="field">
            <span className="field__label">{t('recommendation.filterRisk')}</span>
            <select value={riskLevel} onChange={(e) => setRiskLevel(e.target.value)}>
              {RISKS.map((value) => (
                <option key={value} value={value}>
                  {value ? t(`enums.risk.${value}`) : t('app.all')}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span className="field__label">{t('recommendation.filterMinConfidence')}</span>
            <input
              type="number"
              min="0"
              max="100"
              value={minConfidence}
              onChange={(e) => setMinConfidence(e.target.value)}
              placeholder="0"
            />
          </label>
          <label className="field">
            <span className="field__label">{t('recommendation.filterMaxConfidence')}</span>
            <input
              type="number"
              min="0"
              max="100"
              value={maxConfidence}
              onChange={(e) => setMaxConfidence(e.target.value)}
              placeholder="100"
            />
          </label>
          <label className="field">
            <span className="field__label">{t('recommendation.filterDataQuality')}</span>
            <select value={dataQuality} onChange={(e) => setDataQuality(e.target.value)}>
              {DATA_QUALITIES.map((value) => (
                <option key={value} value={value}>
                  {value ? t(`enums.dataQuality.${value}`) : t('app.all')}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span className="field__label">{t('recommendation.filterVisibility')}</span>
            <select value={visibility} onChange={(e) => setVisibility(e.target.value)}>
              <option value="active">{t('recommendation.visibilityActive')}</option>
              <option value="dismissed">{t('recommendation.visibilityDismissed')}</option>
              <option value="all">{t('recommendation.visibilityAll')}</option>
            </select>
          </label>
          <button className="small recommendation-filters__refresh" onClick={reload}>
            {t('app.refresh')}
          </button>
          <button
            className="small danger recommendation-filters__refresh"
            disabled={bulkBusy || !data || data.total === 0 || visibility === 'dismissed'}
            onClick={() => void dismissAll()}
          >
            {t('recommendation.dismissAll')}
          </button>
        </div>
      }
    >
      <AsyncBoundary loading={loading} error={error} onRetry={reload} hasData={!!data}>
        <div className="stack">
          {bulkMessage && <div className="banner banner--ok">{bulkMessage}</div>}
          {data?.items.map((rec) => (
            <RecommendationCard key={rec.id} recommendation={rec} onChanged={reload} allowDismiss />
          ))}
          {data && data.items.length === 0 && <span className="faint">{t('dashboard.noRecommendations')}</span>}
        </div>
      </AsyncBoundary>
    </Card>
  );
}
