import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Badge, Card, Modal } from '../components/common';
import { formatAge, formatCompact, formatNumber, formatPct, formatPrice, toneOf } from '../utils/format';

export function MarketsPage() {
  const { t } = useTranslation();
  const { data, loading, error, reload } = useApi(() => api.markets(), [], 60_000);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [showAdd, setShowAdd] = useState(false);

  const toggle = async (symbol: string, patch: Record<string, boolean>) => {
    setBusy(true);
    try {
      await api.updateMarket(symbol, patch);
      reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const analyse = async (symbol: string) => {
    setBusy(true);
    setMessage(null);
    try {
      const result = await api.analyzeNow(symbol);
      setMessage(result.llm_error ? `${t('markets.analysisStarted')} — ${result.llm_error}` : t('markets.analysisStarted'));
      reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (symbol: string) => {
    if (!window.confirm(t('markets.deleteConfirm'))) return;
    setBusy(true);
    try {
      await api.deleteMarket(symbol);
      reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Card
        title={t('markets.title')}
        actions={
          <div className="row">
            <button className="small" disabled={busy} onClick={() => setShowAdd(true)}>
              {t('markets.addAsset')}
            </button>
            <button
              className="small"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await api.refreshUniverse();
                  reload();
                } catch (err) {
                  setMessage(err instanceof Error ? err.message : String(err));
                } finally {
                  setBusy(false);
                }
              }}
            >
              {t('markets.refreshUniverse')}
            </button>
          </div>
        }
      >
        {message && <div className="banner">{message}</div>}

        <AsyncBoundary loading={loading} error={error} onRetry={reload} hasData={!!data}>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('markets.symbol')}</th>
                  <th className="numeric">{t('markets.price')}</th>
                  <th className="numeric">{t('markets.change24h')}</th>
                  <th className="numeric">{t('markets.volume24h')}</th>
                  <th className="numeric">{t('markets.rsi')}</th>
                  <th>{t('markets.regime')}</th>
                  <th>{t('markets.lastSignal')}</th>
                  <th>{t('markets.enabled')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {data?.items.map((market) => (
                  <tr key={market.id}>
                    <td>
                      <Link to={`/markets/${market.symbol}`}>{market.symbol}</Link>
                      <div className="faint">{market.display_name}</div>
                    </td>
                    <td className="numeric">{formatPrice(market.price)}</td>
                    <td className={`numeric ${toneOf(market.price_change_24h_pct) ?? ''}`}>
                      {formatPct(market.price_change_24h_pct)}
                    </td>
                    <td className="numeric">{formatCompact(market.volume_24h)}</td>
                    <td className="numeric">{formatNumber(market.rsi, 1)}</td>
                    <td>{market.market_regime ? t(`enums.regime.${market.market_regime}`) : '—'}</td>
                    <td>
                      {market.last_action ? (
                        <Badge
                          tone={
                            market.last_action === 'OPEN_LONG'
                              ? 'long'
                              : market.last_action === 'OPEN_SHORT'
                                ? 'short'
                                : undefined
                          }
                        >
                          {t(`enums.action.${market.last_action}`)} · {formatAge(market.last_signal_at)}
                        </Badge>
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`${t('markets.enabled')}: ${market.symbol}`}
                        checked={market.enabled}
                        disabled={busy}
                        onChange={(e) => void toggle(market.symbol, { enabled: e.target.checked })}
                      />
                      {market.pinned && <Badge tone="accent">{t('markets.pinned')}</Badge>}
                      {market.manually_added && <Badge>{t('markets.manual')}</Badge>}
                    </td>
                    <td>
                      <div className="row">
                        <button className="small" disabled={busy} onClick={() => void analyse(market.symbol)}>
                          {t('markets.analyzeNow')}
                        </button>
                        <button
                          className="small ghost"
                          aria-label={`${t('markets.pinned')}: ${market.symbol}`}
                          title={t('markets.pinned')}
                          disabled={busy}
                          onClick={() => void toggle(market.symbol, { pinned: !market.pinned })}
                        >
                          📌
                        </button>
                        <button
                          className="small ghost danger"
                          aria-label={`${t('app.delete')}: ${market.symbol}`}
                          title={t('app.delete')}
                          disabled={busy}
                          onClick={() => void remove(market.symbol)}
                        >
                          ✕
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </AsyncBoundary>
      </Card>

      {showAdd && (
        <AddAssetModal
          onClose={() => setShowAdd(false)}
          onCreated={() => {
            setShowAdd(false);
            reload();
          }}
        />
      )}
    </>
  );
}

function AddAssetModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { t } = useTranslation();
  const [form, setForm] = useState({ coingecko_id: '', symbol: '', display_name: '', pinned: true });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.createMarket(form);
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={t('markets.addAsset')} onClose={onClose}>
      <p className="faint">{t('markets.addAssetHint')}</p>
      <label className="field">
        <span className="field__label">{t('markets.coingeckoId')}</span>
        <input
          value={form.coingecko_id}
          onChange={(e) => setForm({ ...form, coingecko_id: e.target.value })}
          placeholder="bitcoin"
        />
      </label>
      <label className="field">
        <span className="field__label">{t('markets.symbol')}</span>
        <input
          value={form.symbol}
          onChange={(e) => setForm({ ...form, symbol: e.target.value })}
          placeholder="BTC"
        />
      </label>
      <label className="field">
        <span className="field__label">{t('app.edit')}</span>
        <input value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })} />
      </label>
      <label className="field field--inline">
        <input
          type="checkbox"
          checked={form.pinned}
          onChange={(e) => setForm({ ...form, pinned: e.target.checked })}
        />
        <span>{t('markets.pinned')}</span>
      </label>

      {error && <div className="banner banner--error">{error}</div>}

      <div className="row row--between">
        <button className="ghost" onClick={onClose}>
          {t('app.cancel')}
        </button>
        <button className="primary" disabled={busy} onClick={() => void submit()}>
          {t('app.add')}
        </button>
      </div>
    </Modal>
  );
}
