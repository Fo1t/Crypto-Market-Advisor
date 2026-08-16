import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type { Recommendation } from '../api/types';
import { Badge } from './common';
import { OpenPositionModal } from './OpenPositionModal';
import { formatAge, formatNumber, formatPrice } from '../utils/format';
import type { TFunction } from 'i18next';

const TONE: Record<string, 'long' | 'short' | 'neutral'> = {
  OPEN_LONG: 'long',
  OPEN_SHORT: 'short',
  NO_ACTION: 'neutral',
  MANAGE_POSITION: 'neutral',
};

const ASCII_LETTERS = /[A-Za-z]/g;

function isLikelyLanguageMismatch(text: string, language: 'ru' | 'en' | 'zh-CN'): boolean {
  const ascii = text.match(ASCII_LETTERS)?.length ?? 0;
  const cyrillic = text.match(/[\u0400-\u04ff]/g)?.length ?? 0;
  const cjk = text.match(/[\u3400-\u9fff]/g)?.length ?? 0;
  if (language === 'en') return cyrillic + cjk >= 12 && cyrillic + cjk > ascii;
  if (language === 'zh-CN') return cjk < 4 && ascii + cyrillic >= 24;
  return ascii >= 24 && ascii > cyrillic * 2;
}

function translateRiskNote(note: string, language: string, t: TFunction): string {
  if (language === 'en') return note;
  const dataQuality = note.match(/^data quality is (ok|degraded|unusable)$/);
  if (dataQuality) {
    return t('recommendation.riskNote.dataQuality', { quality: t(`enums.dataQuality.${dataQuality[1]}`) });
  }
  const rules: Array<[RegExp, string, string[]]> = [
    [/^stop distance ([\d.]+)% caps leverage at ([\d.]+)x \(max ([\d.]+)% loss on margin\)$/, 'stopDistance', ['distance', 'leverage', 'loss']],
    [/^extreme volatility \(ATR ([\d.]+)%\) caps leverage at ([\d.]+)x$/, 'extremeVolatility', ['atr', 'leverage']],
    [/^elevated volatility \(ATR ([\d.]+)%\) caps leverage at ([\d.]+)x$/, 'elevatedVolatility', ['atr', 'leverage']],
    [/^volatility is in the ([\d.]+)th percentile of recent history$/, 'volatilityPercentile', ['percentile']],
    [/^confidence (\d+) is below the (\d+) threshold$/, 'confidenceBelow', ['confidence', 'threshold']],
    [/^timeframes disagree \(alignment (-?[\d.]+)\)$/, 'timeframesDisagree', ['alignment']],
    [/^opposing level ([\d.]+)% away limits the runway$/, 'opposingLevel', ['distance']],
    [/^model suggested (\d+)x, risk-adjusted to (\d+)x$/, 'modelAdjusted', ['suggested', 'adjusted']],
    [/^allocation reduced from ([\d.]+)% to the configured maximum ([\d.]+)%$/, 'allocationMaximum', ['from', 'to']],
    [/^allocation scaled to ([\d.]+)% for confidence (\d+)$/, 'allocationConfidence', ['allocation', 'confidence']],
  ];
  for (const [pattern, key, names] of rules) {
    const match = note.match(pattern);
    if (!match) continue;
    return t(`recommendation.riskNote.${key}`, {
      ...Object.fromEntries(names.map((name, index) => [name, match[index + 1]])),
    });
  }
  const exact: Record<string, string> = {
    'no usable stop distance: leverage capped conservatively': 'noStopDistance',
    'ATR unavailable: leverage reduced for unknown volatility': 'atrUnavailable',
    'uncertain regime reduces allowed leverage': 'uncertainRegime',
    'range regime reduces allowed leverage': 'rangeRegime',
    'breakout regime carries retest risk': 'breakoutRegime',
    'high volatility tag reduces allowed leverage': 'highVolatilityTag',
    'moderate confidence caps leverage at 20x': 'moderateConfidence',
    'allocation scaled down because allowed leverage is at the floor': 'allocationFloor',
  };
  return t(`recommendation.riskNote.${exact[note] ?? 'technicalAdjustment'}`);
}

function OriginalNarrative({ recommendation }: { recommendation: Recommendation }) {
  const { t } = useTranslation();
  return (
    <details className="original-narrative">
      <summary>{t('recommendation.showOriginalText')}</summary>
      <p className="muted">{recommendation.summary}</p>
      {recommendation.signals_for.length > 0 && (
        <>
          <h3>{t('recommendation.signalsFor')}</h3>
          <ul className="signal-list">
            {recommendation.signals_for.map((signal, index) => <li key={`for-${index}`}>{signal}</li>)}
          </ul>
        </>
      )}
      {recommendation.signals_against.length > 0 && (
        <>
          <h3>{t('recommendation.signalsAgainst')}</h3>
          <ul className="signal-list">
            {recommendation.signals_against.map((signal, index) => <li key={`against-${index}`}>{signal}</li>)}
          </ul>
        </>
      )}
      {recommendation.invalidation_conditions.length > 0 && (
        <>
          <h3>{t('recommendation.invalidation')}</h3>
          <ul className="signal-list">
            {recommendation.invalidation_conditions.map((condition, index) => <li key={`invalid-${index}`}>{condition}</li>)}
          </ul>
        </>
      )}
      {recommendation.risk_engine_notes && recommendation.risk_engine_notes.length > 0 && (
        <>
          <h3>{t('recommendation.riskNotes')}</h3>
          <ul className="signal-list">
            {recommendation.risk_engine_notes.map((note, index) => <li key={`risk-${index}`}>{note}</li>)}
          </ul>
        </>
      )}
    </details>
  );
}

/**
 * RecommendationCard renders one advisory. It shows both the model's leverage
 * suggestion and the risk-adjusted value, because hiding the difference would
 * hide the whole point of the risk engine.
 */
export function RecommendationCard({
  recommendation,
  onChanged,
  compact = false,
  allowDismiss = false,
}: {
  recommendation: Recommendation;
  onChanged?: () => void;
  compact?: boolean;
  allowDismiss?: boolean;
}) {
  const { t, i18n } = useTranslation();
  const [busy, setBusy] = useState(false);
  const [openModal, setOpenModal] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  const tone = TONE[recommendation.action] ?? 'neutral';
  const isEntry = recommendation.action === 'OPEN_LONG' || recommendation.action === 'OPEN_SHORT';
  const decided = recommendation.decision?.decision;
  const language = i18n.language === 'en' || i18n.language === 'zh-CN' ? i18n.language : 'ru';
  const translation = recommendation.translations?.[language];
  const summary = translation?.summary ?? recommendation.summary;
  const signalsFor = translation?.signals_for ?? recommendation.signals_for;
  const signalsAgainst = translation?.signals_against ?? recommendation.signals_against;
  const invalidation = translation?.invalidation_conditions ?? recommendation.invalidation_conditions;
  const narrativeText = [
    summary,
    ...signalsFor,
    ...signalsAgainst,
    ...invalidation,
    ...(translation?.management_reasons ?? recommendation.management?.actions.map((action) => action.reason) ?? []),
  ].join(' ');
  const languageMismatch = !translation && isLikelyLanguageMismatch(narrativeText, language);

  const decide = async (decision: string) => {
    setBusy(true);
    try {
      await api.decide(recommendation.id, decision);
      setMessage(t('recommendation.decisionSaved'));
      onChanged?.();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const changeVisibility = async (dismiss: boolean) => {
    if (dismiss && !window.confirm(t('recommendation.dismissConfirm'))) return;
    setBusy(true);
    setMessage(null);
    try {
      if (dismiss) {
        await api.dismissRecommendation(recommendation.id);
        setMessage(t('recommendation.dismissed'));
      } else {
        await api.restoreRecommendation(recommendation.id);
        setMessage(t('recommendation.restored'));
      }
      onChanged?.();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <article className={`rec rec--${tone}${recommendation.dismissed_at ? ' rec--dismissed' : ''}`}>
      <div className="rec__head">
        <div className="stack" style={{ gap: 4 }}>
          <span className="rec__symbol">{recommendation.symbol}</span>
          <span className={`rec__action ${tone === 'neutral' ? 'muted' : tone}`}>
            {t(`enums.action.${recommendation.action}`)}
          </span>
        </div>
        <div className="row">
          <Badge tone="accent">
            {t('recommendation.confidence')}: {recommendation.confidence}%
          </Badge>
          <Badge tone={recommendation.risk_level === 'low' ? 'long' : recommendation.risk_level === 'medium' ? 'warn' : 'short'}>
            {t('recommendation.risk')}: {t(`enums.risk.${recommendation.risk_level}`)}
          </Badge>
          <Badge tone={recommendation.freshness === 'fresh' ? 'long' : 'warn'}>
            {t(`recommendation.${recommendation.freshness}`)}
          </Badge>
          <span className="faint">{formatAge(recommendation.created_at)}</span>
        </div>
      </div>

      <div className="rec__body">
        {languageMismatch ? (
          <div className="banner narrative-language">
            <span>{t('recommendation.originalLanguageNotice')}</span>
            {!compact && <OriginalNarrative recommendation={recommendation} />}
          </div>
        ) : (
          <p className="muted" style={{ margin: 0 }}>
            {summary}
          </p>
        )}

        {isEntry && (
          <div className="grid grid--4">
            <div className="stat">
              <span className="stat__label">{t('recommendation.leverage')}</span>
              <span className="stat__value">{recommendation.leverage.recommended}x</span>
              <span className="stat__hint">
                {t('recommendation.llmSuggestion')}: {recommendation.leverage.llm_suggested}x ·{' '}
                {t('recommendation.riskAdjusted')}: {recommendation.leverage.risk_maximum}x
              </span>
            </div>
            <div className="stat">
              <span className="stat__label">{t('recommendation.allocation')}</span>
              <span className="stat__value">{formatNumber(recommendation.recommended_allocation_pct, 1)}%</span>
              <span className="stat__hint">{t('recommendation.entry')}</span>
            </div>
            <div className="stat">
              <span className="stat__label">{t('recommendation.entryZone')}</span>
              <span className="stat__value">
                {recommendation.entry?.preferred_min && recommendation.entry?.preferred_max
                  ? `${formatPrice(recommendation.entry.preferred_min)} – ${formatPrice(recommendation.entry.preferred_max)}`
                  : formatPrice(recommendation.reference_price)}
              </span>
              <span className="stat__hint">{recommendation.entry?.type ?? 'market'}</span>
            </div>
            <div className="stat">
              <span className="stat__label">{t('market.regime')}</span>
              <span className="stat__value" style={{ fontSize: 14 }}>
                {recommendation.market_regime ? t(`enums.regime.${recommendation.market_regime}`) : '—'}
              </span>
              <span className="stat__hint">
                {t('market.dataQuality')}: {t(`enums.dataQuality.${recommendation.data_quality}`)}
              </span>
            </div>
          </div>
        )}

        {isEntry && !compact && (
          <div className="grid grid--2">
            <div className="stack" style={{ gap: 6 }}>
              <h3>{t('recommendation.takeProfit')}</h3>
              <div className="levels">
                {recommendation.take_profit.map((tp, i) => (
                  <div className="level level--tp" key={`tp-${i}`}>
                    <span>TP{i + 1} · {formatPrice(tp.price)}</span>
                    <span className="muted">
                      {t('recommendation.closePct')} {tp.close_pct}%
                      {translation?.take_profit_reasons[i] ? ` · ${translation.take_profit_reasons[i]}` : ''}
                    </span>
                  </div>
                ))}
              </div>
            </div>
            <div className="stack" style={{ gap: 6 }}>
              <h3>{t('recommendation.stopLoss')}</h3>
              <div className="levels">
                {recommendation.stop_loss.map((sl, i) => (
                  <div className="level level--sl" key={`sl-${i}`}>
                    <span>SL{i + 1} · {formatPrice(sl.price)}</span>
                    <span className="muted">
                      {t('recommendation.closePct')} {sl.close_pct}%
                      {translation?.stop_loss_reasons[i] ? ` · ${translation.stop_loss_reasons[i]}` : ''}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        )}

        {recommendation.management && !compact && (
          <div className="stack" style={{ gap: 6 }}>
            <h3>{t('recommendation.management')}</h3>
            <ul className="signal-list">
              {recommendation.management.actions.map((action, i) => (
                <li key={i}>
                  {t(`enums.management.${action.type}`)}
                  {action.new_stop_loss ? ` → ${formatPrice(action.new_stop_loss)}` : ''}
                  {action.close_pct ? ` → ${action.close_pct}%` : ''}
                  {!languageMismatch && (translation?.management_reasons[i] || action.reason)
                    ? ` — ${translation?.management_reasons[i] ?? action.reason}`
                    : ''}
                </li>
              ))}
            </ul>
          </div>
        )}

        {!compact && !languageMismatch && (
          <div className="grid grid--2">
            <div className="stack" style={{ gap: 6 }}>
              <h3>{t('recommendation.whyThisSignal')}</h3>
              <ul className="signal-list">
                {signalsFor.map((s, i) => (
                  <li key={i} className="long">
                    {s}
                  </li>
                ))}
                {signalsFor.length === 0 && <li>{t('app.empty')}</li>}
              </ul>
            </div>
            <div className="stack" style={{ gap: 6 }}>
              <h3>{t('recommendation.signalsAgainst')}</h3>
              <ul className="signal-list">
                {signalsAgainst.map((s, i) => (
                  <li key={i} className="short">
                    {s}
                  </li>
                ))}
                {signalsAgainst.length === 0 && <li>{t('app.empty')}</li>}
              </ul>
            </div>
          </div>
        )}

        {!compact && !languageMismatch && invalidation.length > 0 && (
          <div className="stack" style={{ gap: 6 }}>
            <h3>{t('recommendation.invalidation')}</h3>
            <ul className="signal-list">
              {invalidation.map((s, i) => (
                <li key={i}>{s}</li>
              ))}
            </ul>
          </div>
        )}

        {!compact && !languageMismatch && recommendation.risk_engine_notes && recommendation.risk_engine_notes.length > 0 && (
          <details>
            <summary className="faint">{t('recommendation.riskNotes')}</summary>
            <ul className="signal-list">
              {recommendation.risk_engine_notes.map((s, i) => (
                <li key={i}>{translateRiskNote(s, language, t)}</li>
              ))}
            </ul>
          </details>
        )}

        {message && <div className="banner banner--ok">{message}</div>}

        <div className="row">
          {isEntry && (
            <>
              <button className="primary" disabled={busy} onClick={() => setOpenModal(true)}>
                {t('recommendation.opened')}
              </button>
              <button disabled={busy} onClick={() => void decide('SKIPPED')}>
                {t('recommendation.skipped')}
              </button>
              <button className="ghost" disabled={busy} onClick={() => void decide('IGNORED')}>
                {t('recommendation.ignored')}
              </button>
            </>
          )}
          {decided && (
            <span className="badge">
              {t('enums.decision.' + decided)}
            </span>
          )}
          {allowDismiss && recommendation.dismissed_at && (
            <button className="small" disabled={busy} onClick={() => void changeVisibility(false)}>
              {t('recommendation.restore')}
            </button>
          )}
          {allowDismiss && !recommendation.dismissed_at && (
            <button className="small danger" disabled={busy} onClick={() => void changeVisibility(true)}>
              {t('recommendation.dismiss')}
            </button>
          )}
          <span className="faint">
            {recommendation.model_name} · {recommendation.prompt_version}
          </span>
        </div>
      </div>

      {openModal && (
        <OpenPositionModal
          recommendation={recommendation}
          onClose={() => setOpenModal(false)}
          onCreated={() => {
            setOpenModal(false);
            setMessage(t('recommendation.decisionSaved'));
            onChanged?.();
          }}
        />
      )}
    </article>
  );
}
