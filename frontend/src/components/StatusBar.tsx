import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type { ComponentStatus } from '../api/types';
import { useApi } from '../hooks/useApi';
import { formatAge } from '../utils/format';

const COMPONENT_LABELS: Record<string, string> = {
  database: 'status.database',
  coingecko: 'status.coingecko',
  market_data: 'status.marketData',
  llm: 'status.llm',
  news: 'status.news',
};

const STATUS_LABELS: Record<ComponentStatus, string> = {
  online: 'status.online',
  degraded: 'status.degraded',
  offline: 'status.offline',
  disabled: 'status.disabled',
};

/**
 * StatusBar shows dependency health and the operational timestamps required for
 * observability: last market update, last analysis, last inference, next run.
 */
export function StatusBar() {
  const { t } = useTranslation();
  const { data } = useApi(() => api.health(), [], 30_000);

  const llm = data?.components.find((c) => c.name === 'llm');
  const llmDown = llm && (llm.status === 'offline' || llm.status === 'disabled');
  const context = llm?.llm_context;

  return (
    <header className="topbar">
      <div className="row">
        {data?.components.map((component) => (
          <span key={component.name} className="badge" title={component.message ?? ''}>
            <span className={`dot dot--${component.status}`} />
            {t(COMPONENT_LABELS[component.name] ?? component.name)}: {t(STATUS_LABELS[component.status])}
          </span>
        ))}
        {llmDown && <span className="badge badge--warn">{t('status.llmUnavailable')}</span>}
        {context && (
          <span
            className={`badge${context.level === 'ok' ? '' : ' badge--warn'}`}
            title={t('status.contextHint', {
              peak: context.peak_prompt_tokens,
              reserved: context.max_output_tokens,
              size: context.context_size,
              samples: context.samples,
            })}
          >
            {t('status.context')}: {context.used_pct}%
            {context.level !== 'ok' && ` · ${t(`status.context_${context.level}`)}`}
          </span>
        )}
      </div>

      <div className="row faint">
        <span>
          {t('status.lastMarketUpdate')}: {formatAge(data?.timestamps?.market_data) || t('app.never')}
        </span>
        <span>
          {t('status.lastAnalysis')}: {formatAge(data?.timestamps?.last_analysis) || t('app.never')}
        </span>
        <span>
          {t('status.lastInference')}: {formatAge(data?.timestamps?.last_inference) || t('app.never')}
        </span>
        {data?.scheduler?.next_analysis_cycle && (
          <span>
            {t('status.nextAnalysis')}:{' '}
            {new Date(data.scheduler.next_analysis_cycle).toLocaleTimeString()}
          </span>
        )}
        {data?.scheduler?.cycle_running && <span className="warn">{t('status.cycleRunning')}</span>}
      </div>
    </header>
  );
}
