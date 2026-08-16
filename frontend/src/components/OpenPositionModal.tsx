import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type { Recommendation } from '../api/types';
import { Modal } from './common';
import { toLocalInput } from '../utils/format';

/**
 * OpenPositionModal records a trade the user already executed on the exchange.
 * The application never opens anything itself: this form is pure bookkeeping.
 */
export function OpenPositionModal({
  recommendation,
  symbol,
  entryPrice,
  onClose,
  onCreated,
}: {
  recommendation?: Recommendation;
  symbol?: string;
  entryPrice?: string | number;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { t } = useTranslation();

  const [form, setForm] = useState({
    symbol: recommendation?.symbol ?? symbol ?? '',
    direction: recommendation?.action === 'OPEN_SHORT' ? 'SHORT' : 'LONG',
    entry_price: recommendation?.reference_price ?? (entryPrice == null ? '' : String(entryPrice)),
    leverage: String(recommendation?.leverage.recommended ?? 5),
    quantity: '',
    notional: '',
    margin: '',
    fee_type: 'taker',
    actual_fee: '',
    opened_at: toLocalInput(new Date()),
    note: '',
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const update = (key: keyof typeof form, value: string) =>
    setForm((prev) => ({ ...prev, [key]: value }));

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.createPosition({
        symbol: form.symbol,
        direction: form.direction,
        entry_price: form.entry_price,
        leverage: form.leverage,
        opened_at: new Date(form.opened_at).toISOString(),
        quantity: form.quantity || null,
        notional: form.notional || null,
        margin: form.margin || null,
        fee_type: form.fee_type,
        actual_fee: form.actual_fee || null,
        recommendation_id: recommendation?.id ?? null,
        take_profit: recommendation?.take_profit ?? [],
        stop_loss: recommendation?.stop_loss ?? [],
        note: form.note,
      });
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={t('positions.recordTrade')} onClose={onClose}>
      <div className="grid grid--2">
        <label className="field">
          <span className="field__label">{t('markets.symbol')}</span>
          <input value={form.symbol} onChange={(e) => update('symbol', e.target.value)} />
        </label>
        <label className="field">
          <span className="field__label">{t('positions.direction')}</span>
          <select value={form.direction} onChange={(e) => update('direction', e.target.value)}>
            <option value="LONG">{t('enums.direction.LONG')}</option>
            <option value="SHORT">{t('enums.direction.SHORT')}</option>
          </select>
        </label>
        <label className="field">
          <span className="field__label">{t('positions.entry')}</span>
          <input value={form.entry_price} onChange={(e) => update('entry_price', e.target.value)} />
        </label>
        <label className="field">
          <span className="field__label">{t('positions.leverage')}</span>
          <input value={form.leverage} onChange={(e) => update('leverage', e.target.value)} />
        </label>
        <label className="field">
          <span className="field__label">{t('positions.quantity')}</span>
          <input value={form.quantity} onChange={(e) => update('quantity', e.target.value)} placeholder="0.25" />
        </label>
        <label className="field">
          <span className="field__label">{t('positions.notional')}</span>
          <input value={form.notional} onChange={(e) => update('notional', e.target.value)} placeholder="5000" />
        </label>
        <label className="field">
          <span className="field__label">{t('positions.margin')}</span>
          <input value={form.margin} onChange={(e) => update('margin', e.target.value)} placeholder="500" />
        </label>
        <label className="field">
          <span className="field__label">{t('positions.feeType')}</span>
          <select value={form.fee_type} onChange={(e) => update('fee_type', e.target.value)}>
            <option value="taker">{t('enums.feeType.taker')}</option>
            <option value="maker">{t('enums.feeType.maker')}</option>
            <option value="custom">{t('enums.feeType.custom')}</option>
          </select>
        </label>
        <label className="field">
          <span className="field__label">{t('positions.actualFee')}</span>
          <input value={form.actual_fee} onChange={(e) => update('actual_fee', e.target.value)} />
        </label>
        <label className="field">
          <span className="field__label">{t('positions.openedAt')}</span>
          <input
            type="datetime-local"
            value={form.opened_at}
            onChange={(e) => update('opened_at', e.target.value)}
          />
        </label>
      </div>

      <label className="field">
        <span className="field__label">{t('positions.note')}</span>
        <input value={form.note} onChange={(e) => update('note', e.target.value)} />
      </label>

      <p className="faint">{t('positions.sizeUnknown')}</p>
      {error && <div className="banner banner--error">{error}</div>}

      <div className="row row--between">
        <button className="ghost" onClick={onClose}>
          {t('app.cancel')}
        </button>
        <button className="primary" disabled={busy} onClick={() => void submit()}>
          {t('app.save')}
        </button>
      </div>
    </Modal>
  );
}
