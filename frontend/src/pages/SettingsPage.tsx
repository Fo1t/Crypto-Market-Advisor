import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import { LLM_PRESETS, detectPreset, presetById, type LLMPresetId } from '../api/llmPresets';
import { TIMEFRAMES, type Settings } from '../api/types';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Card } from '../components/common';
import { HistoryImport } from '../components/HistoryImport';
import { LanguageSwitcher } from '../components/LanguageSwitcher';
import { StrategyEditor } from '../components/StrategyEditor';
import { setLanguage, type Language } from '../i18n';

const ALL_TIMEFRAMES = TIMEFRAMES;

export function SettingsPage() {
  const { t, i18n } = useTranslation();
  const { data, loading, error, reload } = useApi(() => api.settings(), []);
  const [draft, setDraft] = useState<Settings | null>(null);
  const [message, setMessage] = useState<{ text: string; ok: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  // Whether the user explicitly chose "custom". The preset is otherwise derived
  // from the endpoint, and "custom" fills nothing in - so without this the
  // button could never appear selected, because nothing about the form changes
  // when it is pressed.
  const [customLLM, setCustomLLM] = useState(false);

  useEffect(() => {
    if (!data) return;
    const fresh = structuredClone(data);
    // The language in effect is the one the UI is speaking, which the sidebar
    // switcher can have changed since these settings were saved. Showing the
    // stored value instead would put the form at odds with the whole screen.
    fresh.general.language = i18n.language;
    setDraft(fresh);
    setCustomLLM(false);
    // The language is deliberately not a dependency: it is read once per load,
    // and re-cloning on a language change would discard unsaved edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  // The sidebar switcher changes the language directly, without going through
  // this form. The form has to follow it, or the two controls disagree - and
  // the next save would quietly restore the language the user just left.
  useEffect(() => {
    setDraft((prev) =>
      prev && prev.general.language !== i18n.language
        ? { ...prev, general: { ...prev.general, language: i18n.language } }
        : prev,
    );
  }, [i18n.language]);

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    setMessage(null);
    try {
      const saved = await api.updateSettings(draft);
      setDraft(structuredClone(saved));
      setLanguage(saved.general.language as Language);
      setMessage({ text: t('settings.saved'), ok: true });
      reload();
    } catch (err) {
      setMessage({ text: err instanceof Error ? err.message : t('settings.saveFailed'), ok: false });
    } finally {
      setBusy(false);
    }
  };

  // Only the nested object sections are patchable; `updated_at` is server-owned.
  type Section = 'general' | 'llm' | 'risk' | 'news' | 'exchange';

  const patch = <K extends Section>(section: K, values: Partial<Settings[K]>) => {
    setDraft((prev) => (prev ? { ...prev, [section]: { ...prev[section], ...values } } : prev));
  };

  // The selected preset is derived from the endpoint rather than stored, so a
  // configuration edited by hand - or arriving from .env - still shows what it
  // actually points at. An explicit "custom" choice wins over the detection
  // until the endpoint itself names a known provider again.
  const detectedPreset = draft ? detectPreset(draft.llm.base_url) : 'custom';
  const activePreset: LLMPresetId = customLLM ? 'custom' : detectedPreset;

  const applyPreset = (id: LLMPresetId) => {
    setCustomLLM(id === 'custom');
    const values = presetById(id).values;
    // "Custom" fills nothing in: it is the state of having chosen your own
    // endpoint, not a set of values to overwrite the current one with.
    if (Object.keys(values).length > 0) patch('llm', values);
  };

  // Typing an endpoint that belongs to a known provider is itself a choice of
  // that provider, so the manual "custom" state steps aside.
  const setBaseURL = (value: string) => {
    patch('llm', { base_url: value });
    if (detectPreset(value) !== 'custom') setCustomLLM(false);
  };

  return (
    <AsyncBoundary loading={loading} error={error} onRetry={reload} hasData={!!draft}>
      {draft && (
        <>
          <div className="banner">{t('settings.noExchangeIntegration')}</div>
          {message && <div className={message.ok ? 'banner banner--ok' : 'banner banner--error'}>{message.text}</div>}

          <Card title={t('settings.general')} collapsible storageKey="settings.general">
            <div className="grid grid--3">
              <div className="field">
                <span className="field__label">{t('settings.language')}</span>
                <LanguageSwitcher
                  value={draft.general.language as Language}
                  onChange={(language) => {
                    patch('general', { language });
                    // Switch immediately so the form itself reflects the choice;
                    // the value is persisted when the settings are saved.
                    setLanguage(language);
                  }}
                />
              </div>
              <label className="field">
                <span className="field__label">{t('settings.analysisInterval')}</span>
                <input
                  type="number"
                  value={draft.general.analysis_interval_seconds}
                  onChange={(e) => patch('general', { analysis_interval_seconds: Number(e.target.value) })}
                />
              </label>
              <label className="field field--inline">
                <input
                  type="checkbox"
                  checked={draft.general.analysis_enabled}
                  onChange={(e) => patch('general', { analysis_enabled: e.target.checked })}
                />
                <span>{t('settings.analysisEnabled')}</span>
              </label>
            </div>

            <div className="field" style={{ marginTop: 12 }}>
              <span className="field__label">{t('settings.timeframes')}</span>
              <div className="row">
                {ALL_TIMEFRAMES.map((tf) => (
                  <label key={tf} className="field--inline row" style={{ gap: 5 }}>
                    <input
                      type="checkbox"
                      checked={draft.general.timeframes.includes(tf)}
                      onChange={(e) =>
                        patch('general', {
                          timeframes: e.target.checked
                            ? [...draft.general.timeframes, tf]
                            : draft.general.timeframes.filter((v) => v !== tf),
                        })
                      }
                    />
                    <span>{tf}</span>
                  </label>
                ))}
              </div>
            </div>
          </Card>

          <Card title={t('settings.llm')} collapsible storageKey="settings.llm">
            <div className="preset-row">
              <span className="field__label">{t('settings.llmPreset')}</span>
              <div className="tabs">
                {LLM_PRESETS.map((preset) => (
                  <button
                    key={preset.id}
                    type="button"
                    className={activePreset === preset.id ? 'tab tab--active' : 'tab'}
                    onClick={() => applyPreset(preset.id)}
                  >
                    {t(`settings.llmPresets.${preset.id}`)}
                  </button>
                ))}
              </div>
              <span className="faint">{t(`settings.llmPresetHints.${activePreset}`)}</span>
            </div>
            <div className="grid grid--3">
              <label className="field">
                <span className="field__label">{t('settings.baseUrl')}</span>
                <input value={draft.llm.base_url} onChange={(e) => setBaseURL(e.target.value)} />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.model')}</span>
                <input value={draft.llm.model} onChange={(e) => patch('llm', { model: e.target.value })} />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.timeout')}</span>
                <input
                  type="number"
                  value={draft.llm.timeout_seconds}
                  onChange={(e) => patch('llm', { timeout_seconds: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.temperature')}</span>
                <input
                  type="number"
                  step="0.05"
                  value={draft.llm.temperature}
                  onChange={(e) => patch('llm', { temperature: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.maxTokens')}</span>
                <input
                  type="number"
                  min="256"
                  value={draft.llm.max_tokens}
                  onChange={(e) => patch('llm', { max_tokens: Number(e.target.value) })}
                />
                <span className="faint">{t('settings.maxTokensHint')}</span>
              </label>
              <label className="field">
                <span className="field__label">{t('settings.contextSize')}</span>
                <input
                  type="number"
                  min="4096"
                  step="1024"
                  value={draft.llm.context_size}
                  onChange={(e) => patch('llm', { context_size: Number(e.target.value) })}
                />
                <span className="faint">{t('settings.contextSizeHint')}</span>
              </label>
              <label className="field">
                <span className="field__label">{t('settings.maxConcurrent')}</span>
                <input
                  type="number"
                  value={draft.llm.max_concurrent_requests}
                  onChange={(e) => patch('llm', { max_concurrent_requests: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.promptVersion')}</span>
                <input
                  value={draft.llm.prompt_version}
                  onChange={(e) => patch('llm', { prompt_version: e.target.value })}
                />
              </label>
              <label className="field field--inline">
                <input
                  type="checkbox"
                  checked={draft.llm.enabled}
                  onChange={(e) => patch('llm', { enabled: e.target.checked })}
                />
                <span>{t('settings.llmEnabled')}</span>
              </label>
            </div>
          </Card>

          <Card title={t('settings.risk')} collapsible storageKey="settings.risk">
            <div className="grid grid--3">
              <label className="field">
                <span className="field__label">{t('settings.minLeverage')}</span>
                <input
                  type="number"
                  value={draft.risk.min_leverage}
                  onChange={(e) => patch('risk', { min_leverage: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.maxLeverage')}</span>
                <input
                  type="number"
                  value={draft.risk.max_leverage}
                  onChange={(e) => patch('risk', { max_leverage: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.maxAllocation')}</span>
                <input
                  value={draft.risk.max_recommended_allocation_pct}
                  onChange={(e) => patch('risk', { max_recommended_allocation_pct: e.target.value })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.riskPerTrade')}</span>
                <input
                  type="number"
                  step="0.05"
                  min={0}
                  value={draft.risk.risk_per_trade_pct}
                  onChange={(e) => patch('risk', { risk_per_trade_pct: Number(e.target.value) })}
                />
                <span className="faint short">{t('settings.riskPerTradeHint')}</span>
              </label>
              <label className="field">
                <span className="field__label">{t('settings.highVolatility')}</span>
                <input
                  type="number"
                  step="0.1"
                  value={draft.risk.high_volatility_atr_pct}
                  onChange={(e) => patch('risk', { high_volatility_atr_pct: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.extremeVolatility')}</span>
                <input
                  type="number"
                  step="0.1"
                  value={draft.risk.extreme_volatility_atr_pct}
                  onChange={(e) => patch('risk', { extreme_volatility_atr_pct: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.minConfidence')}</span>
                <input
                  type="number"
                  value={draft.risk.min_confidence}
                  onChange={(e) => patch('risk', { min_confidence: Number(e.target.value) })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.criticalNewsMaxLeverage')}</span>
                <input type="number" value={draft.risk.critical_news_max_leverage} onChange={(e) => patch('risk', { critical_news_max_leverage: Number(e.target.value) })} />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.criticalNewsHighVolMaxLeverage')}</span>
                <input type="number" value={draft.risk.critical_news_high_vol_max_leverage} onChange={(e) => patch('risk', { critical_news_high_vol_max_leverage: Number(e.target.value) })} />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.criticalNewsMaxAge')}</span>
                <input type="number" value={draft.risk.critical_news_max_age_seconds} onChange={(e) => patch('risk', { critical_news_max_age_seconds: Number(e.target.value) })} />
              </label>
            </div>
          </Card>

          <Card title={t('settings.news')} collapsible defaultOpen={false} storageKey="settings.news">
            <p className="faint">{t('settings.newsHint')}</p>
            <div className="grid grid--3">
              <label className="field field--inline"><input type="checkbox" checked={draft.news.enabled} onChange={(e) => patch('news', { enabled: e.target.checked })} /><span>{t('settings.newsEnabled')}</span></label>
              <label className="field field--inline"><input type="checkbox" checked={draft.news.bybit_enabled} onChange={(e) => patch('news', { bybit_enabled: e.target.checked })} /><span>{t('settings.bybitNewsEnabled')}</span></label>
              <label className="field"><span className="field__label">{t('settings.newsFetchInterval')}</span><input type="number" min="60" value={draft.news.fetch_interval_seconds} onChange={(e) => patch('news', { fetch_interval_seconds: Number(e.target.value) })} /></label>
              <label className="field"><span className="field__label">{t('settings.newsLookback')}</span><input type="number" min="1" max="168" value={draft.news.llm_lookback_hours} onChange={(e) => patch('news', { llm_lookback_hours: Number(e.target.value) })} /></label>
              <label className="field"><span className="field__label">{t('settings.newsAssetItems')}</span><input type="number" min="1" max="100" value={draft.news.llm_max_asset_items} onChange={(e) => patch('news', { llm_max_asset_items: Number(e.target.value) })} /></label>
              <label className="field"><span className="field__label">{t('settings.newsGlobalItems')}</span><input type="number" min="1" max="100" value={draft.news.llm_max_global_items} onChange={(e) => patch('news', { llm_max_global_items: Number(e.target.value) })} /></label>
              <label className="field"><span className="field__label">{t('settings.newsHistorySample')}</span><input type="number" min="1" max="1000" value={draft.news.history_min_sample_size} onChange={(e) => patch('news', { history_min_sample_size: Number(e.target.value) })} /></label>
            </div>
          </Card>

          <Card title={t('settings.exchange')} collapsible storageKey="settings.exchange">
            <p className="faint">{t('settings.feesHint')}</p>
            <div className="grid grid--3">
              <label className="field">
                <span className="field__label">{t('settings.exchangeName')}</span>
                <input
                  value={draft.exchange.exchange}
                  onChange={(e) => patch('exchange', { exchange: e.target.value })}
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.makerFee')}</span>
                <input
                  value={draft.exchange.maker_fee_pct ?? ''}
                  onChange={(e) => patch('exchange', { maker_fee_pct: e.target.value || null })}
                  placeholder="0.02"
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.takerFee')}</span>
                <input
                  value={draft.exchange.taker_fee_pct ?? ''}
                  onChange={(e) => patch('exchange', { taker_fee_pct: e.target.value || null })}
                  placeholder="0.055"
                />
              </label>
              <label className="field">
                <span className="field__label">{t('settings.slippage')}</span>
                <input
                  value={draft.exchange.slippage_pct}
                  onChange={(e) => patch('exchange', { slippage_pct: e.target.value })}
                />
              </label>
            </div>
          </Card>

          <Card title={t('settings.strategies')} collapsible defaultOpen={false} storageKey="settings.strategies">
            <p className="faint short">{t('settings.strategiesHint')}</p>
            <StrategyEditor
              value={draft.strategies ?? { min_signal: 1, items: [] }}
              onChange={(next) => setDraft((prev) => (prev ? { ...prev, strategies: next } : prev))}
            />
          </Card>

          <Card title={t('settings.history')} collapsible defaultOpen={false} storageKey="settings.history">
            <HistoryImport />
          </Card>

          <Card title={t('settings.maintenance')} collapsible defaultOpen={false} storageKey="settings.maintenance">
            <PurgeBacktests />
          </Card>

          <div className="row">
            <button className="primary" disabled={busy} onClick={() => void save()}>
              {t('app.save')}
            </button>
          </div>
        </>
      )}
    </AsyncBoundary>
  );
}

/**
 * PurgeBacktests permanently removes the runs that were already hidden.
 *
 * It lives in the settings rather than next to the run list on purpose: hiding a
 * run is reversible and belongs where the runs are, while this frees the space
 * and cannot be undone. It also states what it is about to remove, because
 * asking for confirmation without saying the size is asking for a blind yes.
 */
function PurgeBacktests() {
  const { t } = useTranslation();
  const hidden = useApi(() => api.hiddenBacktests(), [], 0);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<number | null>(null);

  const purge = async () => {
    setBusy(true);
    try {
      const result = await api.purgeBacktests();
      setDone(result.removed);
      hidden.reload();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };

  const stats = hidden.data;
  const nothing = !stats || stats.runs === 0;

  return (
    <>
      <p className="faint short">{t('settings.purgeHint')}</p>
      <p className="short">
        {nothing
          ? t('settings.purgeEmpty')
          : t('settings.purgeSummary', {
              runs: stats.runs,
              trades: stats.trades,
              size: stats.size,
            })}
      </p>
      {done !== null && <div className="banner">{t('settings.purgeDone', { count: done })}</div>}
      {!nothing &&
        (confirming ? (
          <div className="row">
            <button className="danger" disabled={busy} onClick={() => void purge()}>
              {t('settings.purgeConfirm')}
            </button>
            <button className="ghost" disabled={busy} onClick={() => setConfirming(false)}>
              {t('app.cancel')}
            </button>
          </div>
        ) : (
          <button className="ghost" onClick={() => setConfirming(true)}>
            {t('settings.purge')}
          </button>
        ))}
    </>
  );
}
