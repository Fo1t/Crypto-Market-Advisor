import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type {
  StrategyCatalogItem,
  StrategyConfig,
  StrategyPreset,
  StrategySet,
  StrategySides,
} from '../api/types';
import { useApi } from '../hooks/useApi';

/**
 * StrategyEditor edits one deterministic policy: the entry threshold plus the
 * switch, weight and hard veto of every strategy. The settings screen edits the
 * saved policy with it; the backtest form edits a copy that applies to a single
 * run, which is what comparing two weight profiles over one period needs.
 */
export function StrategyEditor({
  value,
  onChange,
  compact = false,
}: {
  value: StrategySet;
  onChange: (next: StrategySet) => void;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  const catalog = useApi(() => api.strategyCatalog(), []);
  const items = catalog.data?.items ?? [];
  const presets = catalog.data?.presets ?? [];

  const configOf = (item: StrategyCatalogItem): StrategyConfig =>
    value.items.find((entry) => entry.id === item.id) ?? {
      id: item.id,
      enabled: item.default_enabled,
      weight: item.default_weight,
      hard_veto: item.default_hard_veto,
    };

  const update = (item: StrategyCatalogItem, changes: Partial<StrategyConfig>) => {
    const current = configOf(item);
    const next = value.items.some((entry) => entry.id === item.id)
      ? value.items.map((entry) => (entry.id === item.id ? { ...entry, ...changes } : entry))
      : [...value.items, { ...current, ...changes }];
    onChange({ ...value, items: next });
  };

  return (
    <>
      {presets.length > 0 && (
        <PresetPicker presets={presets} value={value} onChange={onChange} compact={compact} />
      )}

      <label className="field" style={{ maxWidth: 260 }}>
        <span className="field__label">{t('settings.minSignal')}</span>
        <input
          type="number"
          step="0.1"
          min={0}
          value={value.min_signal}
          onChange={(e) => onChange({ ...value, min_signal: Number(e.target.value) })}
        />
        <span className="faint short">{t('settings.minSignalHint')}</span>
      </label>

      <label className="field" style={{ maxWidth: 260, marginTop: 10 }}>
        <span className="field__label">{t('settings.sides')}</span>
        <select
          value={value.sides ?? 'long'}
          onChange={(e) => onChange({ ...value, sides: e.target.value as StrategySides })}
        >
          {(['both', 'long', 'short'] as const).map((side) => (
            <option key={side} value={side}>
              {t(`settings.sidesOption.${side}`)}
            </option>
          ))}
        </select>
        <span className="faint short">{t('settings.sidesHint')}</span>
      </label>

      <label className="field field--inline" style={{ marginTop: 10 }}>
        <input
          type="checkbox"
          checked={value.regime_adaptive ?? false}
          onChange={(e) => onChange({ ...value, regime_adaptive: e.target.checked })}
        />
        <span>{t('settings.regimeAdaptive')}</span>
      </label>
      {!compact && <p className="faint short">{t('settings.regimeAdaptiveHint')}</p>}

      {(['directional', 'filter'] as const).map((kind) => {
        const group = items.filter((item) => item.kind === kind);
        if (group.length === 0) return null;
        return (
          <div key={kind} style={{ marginTop: 14 }}>
            <h3 className="short">{t(`settings.strategyKind.${kind}`)}</h3>
            {!compact && <p className="faint short">{t(`settings.strategyKindHint.${kind}`)}</p>}
            {!compact && kind === 'directional' && (
              <p className="faint short">{t('settings.negativeWeightHint')}</p>
            )}
            <div className="strategy-list">
              {group.map((item) => {
                const config = configOf(item);
                return (
                  <div key={item.id} className="strategy-row">
                    <label className="field field--inline">
                      <input
                        type="checkbox"
                        checked={config.enabled}
                        onChange={(e) => update(item, { enabled: e.target.checked })}
                      />
                      <span>{t(`strategies.${item.id}`, { defaultValue: item.id })}</span>
                    </label>
                    {compact ? (
                      <span />
                    ) : (
                      <span className="faint short strategy-row__desc">
                        {t(`strategiesHint.${item.id}`, { defaultValue: '' })}
                      </span>
                    )}
                    <label className="field strategy-row__weight">
                      <span className="field__label">{t('settings.weight')}</span>
                      <input
                        type="number"
                        step="0.1"
                        min={kind === 'filter' ? 0 : -100}
                        value={config.weight}
                        disabled={!config.enabled}
                        onChange={(e) => update(item, { weight: Number(e.target.value) })}
                      />
                    </label>
                    {kind === 'filter' ? (
                      <label className="field field--inline strategy-row__veto">
                        <input
                          type="checkbox"
                          checked={config.hard_veto ?? false}
                          disabled={!config.enabled}
                          onChange={(e) => update(item, { hard_veto: e.target.checked })}
                        />
                        <span>{t('settings.hardVeto')}</span>
                      </label>
                    ) : (
                      <span />
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        );
      })}
    </>
  );
}

/**
 * PresetPicker offers the whole measured profiles rather than a list of names.
 *
 * A user choosing between fourteen strategies has no way to know which
 * combination was ever tested; these were replayed over five years of four-hour
 * bars and four separate years of daily ones, and each carries the figures it
 * produced. Picking one replaces the directional side of the policy and leaves
 * the safety vetoes alone, so a choice here cannot switch off a hard veto by
 * accident.
 */
function PresetPicker({
  presets,
  value,
  onChange,
  compact,
}: {
  presets: StrategyPreset[];
  value: StrategySet;
  onChange: (next: StrategySet) => void;
  compact: boolean;
}) {
  const { t } = useTranslation();

  // A preset counts as active when every weight it names matches what is set.
  const activeId = presets.find((preset) =>
    preset.set.items.every((item) => {
      const current = value.items.find((entry) => entry.id === item.id);
      if (!current) return false;
      return current.enabled === item.enabled && Math.abs(current.weight - item.weight) < 1e-9;
    }) && Math.abs(value.min_signal - preset.set.min_signal) < 1e-9,
  )?.id;

  const apply = (preset: StrategyPreset) => {
    onChange({
      ...value,
      min_signal: preset.set.min_signal,
      sides: preset.set.sides ?? value.sides,
      items: preset.set.items.map((item) => ({ ...item })),
    });
  };

  return (
    <div style={{ marginBottom: 14 }}>
      <h3 className="short">{t('settings.presets')}</h3>
      {!compact && <p className="faint short">{t('settings.presetsHint')}</p>}
      <div className="strategy-list">
        {presets.map((preset) => (
          <button
            key={preset.id}
            type="button"
            className={`preset-card${activeId === preset.id ? ' preset-card--active' : ''}`}
            onClick={() => apply(preset)}
          >
            <strong>{t(`presets.${preset.id}`, { defaultValue: preset.id })}</strong>
            {preset.default && <span className="badge">{t('settings.presetDefault')}</span>}
            <span className="faint short">
              {t(`presetsHint.${preset.id}`, { defaultValue: '' })}
            </span>
            <span className="mono short">
              {t('settings.presetMetrics', {
                pf4h: preset.profit_factor_4h.toFixed(2),
                pf1d: preset.profit_factor_1d.toFixed(2),
                worst: Math.min(preset.worst_window_4h, preset.worst_window_1d).toFixed(2),
                trades: preset.trades_4h + preset.trades_1d,
              })}
            </span>
          </button>
        ))}
      </div>
      {activeId === undefined && (
        <p className="faint short">{t('settings.presetCustom')}</p>
      )}
    </div>
  );
}
