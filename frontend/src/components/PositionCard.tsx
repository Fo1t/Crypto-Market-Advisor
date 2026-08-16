import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type { PositionView, PriceTarget } from '../api/types';
import { Badge, Modal } from './common';
import {
  formatDateTime,
  formatMinutes,
  formatMoney,
  formatNumber,
  formatPrice,
  toLocalInput,
  toneOf,
} from '../utils/format';

type Action = 'close' | 'partial' | 'plan' | 'fee' | 'funding' | null;

/** PositionCard shows one position and exposes every manual bookkeeping action. */
export function PositionCard({ view, onChanged }: { view: PositionView; onChanged: () => void }) {
  const { t, i18n } = useTranslation();
  const [action, setAction] = useState<Action>(null);

  const p = view.position;
  const pnl = view.pnl;
  const isOpen = p.status !== 'CLOSED';
  const directionTone = p.direction === 'LONG' ? 'long' : 'short';

  return (
    <article className={`rec rec--${directionTone}`}>
      <div className="rec__head">
        <div className="stack" style={{ gap: 4 }}>
          <span className="rec__symbol">{p.symbol}</span>
          <span className={`rec__action ${directionTone}`}>{t(`enums.direction.${p.direction}`)}</span>
        </div>
        <div className="row">
          <Badge>{t(`enums.status.${p.status}`)}</Badge>
          <Badge tone="accent">{formatNumber(p.leverage, 0)}x</Badge>
          {!p.size_known && <Badge tone="warn">{t('app.approximate')}</Badge>}
          {!pnl.fees_configured && <Badge tone="warn">{t('status.feesNotConfigured')}</Badge>}
          <span className="faint">{formatMinutes(view.age_minutes)}</span>
        </div>
      </div>

      <div className="rec__body">
        <div className="grid grid--4">
          <div className="stat">
            <span className="stat__label">{t('positions.entry')}</span>
            <span className="stat__value">{formatPrice(p.entry_price)}</span>
          </div>
          <div className="stat">
            <span className="stat__label">{t('positions.current')}</span>
            <span className="stat__value">{formatPrice(view.current_price)}</span>
          </div>
          <div className="stat">
            <span className="stat__label">{t('positions.unrealized')}</span>
            <span className={`stat__value ${toneOf(pnl.leveraged_roi_pct) ?? ''}`}>
              {formatNumber(pnl.leveraged_roi_pct, 2)}%
            </span>
            <span className="stat__hint">
              {p.size_known ? formatMoney(pnl.unrealized_pnl) : t('app.approximate')}
            </span>
          </div>
          <div className="stat">
            <span className="stat__label">{t('positions.realized')}</span>
            <span className={`stat__value ${toneOf(pnl.net_realized_pnl) ?? ''}`}>
              {p.size_known ? formatMoney(pnl.net_realized_pnl) : '—'}
            </span>
            <span className="stat__hint">
              {t('positions.remaining')}: {formatNumber(pnl.remaining_pct, 1)}%
            </span>
          </div>
        </div>

        <div className="grid grid--4">
          <div className="stat">
            <span className="stat__label">{t('positions.fees')}</span>
            <span className="stat__value" style={{ fontSize: 15 }}>
              {formatMoney(pnl.fees)}
            </span>
          </div>
          <div className="stat">
            <span className="stat__label">{t('positions.funding')}</span>
            <span className="stat__value" style={{ fontSize: 15 }}>
              {formatMoney(pnl.funding)}
            </span>
          </div>
          <div className="stat">
            <span className="stat__label">{t('positions.total')}</span>
            <span className={`stat__value ${toneOf(pnl.total_pnl) ?? ''}`} style={{ fontSize: 15 }}>
              {p.size_known ? formatMoney(pnl.total_pnl) : '—'}
            </span>
          </div>
          <div className="stat">
            <span className="stat__label">{t('positions.roi')}</span>
            <span className={`stat__value ${toneOf(pnl.roi_on_margin_pct) ?? ''}`} style={{ fontSize: 15 }}>
              {pnl.roi_on_margin_pct ? `${formatNumber(pnl.roi_on_margin_pct, 2)}%` : '—'}
            </span>
          </div>
        </div>

        <div className="grid grid--2">
          <PlanView title={t('positions.originalPlan')} plan={p.original_plan} />
          <PlanView title={t('positions.currentPlan')} plan={p.current_plan} />
        </div>

        {isOpen && (
          <div className="row">
            <button className="small" onClick={() => setAction('partial')}>
              {t('positions.closePartial')}
            </button>
            <button className="small primary" onClick={() => setAction('close')}>
              {t('positions.closeFull')}
            </button>
            <button className="small" onClick={() => setAction('plan')}>
              {t('positions.editPlan')}
            </button>
            <button className="small" onClick={() => setAction('fee')}>
              {t('positions.addFee')}
            </button>
            <button className="small" onClick={() => setAction('funding')}>
              {t('positions.addFunding')}
            </button>
          </div>
        )}

        {view.events && view.events.length > 0 && (
          <details>
            <summary className="faint">{t('positions.history')}</summary>
            <div className="table-wrap">
              <table>
                <tbody>
                  {view.events.map((event) => (
                    <tr key={event.id}>
                      <td>{formatDateTime(event.occurred_at, i18n.language)}</td>
                      <td>
                        {t(`enums.positionEvent.${event.event_type}`, {
                          defaultValue: event.event_type,
                        })}
                      </td>
                      <td className="faint">{JSON.stringify(event.payload)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        )}
      </div>

      {action && (
        <PositionActionModal
          view={view}
          action={action}
          onClose={() => setAction(null)}
          onDone={() => {
            setAction(null);
            onChanged();
          }}
        />
      )}
    </article>
  );
}

function PlanView({ title, plan }: { title: string; plan?: { take_profit: PriceTarget[]; stop_loss: PriceTarget[] } }) {
  const { t } = useTranslation();
  if (!plan || (plan.take_profit.length === 0 && plan.stop_loss.length === 0)) {
    return (
      <div className="stack" style={{ gap: 6 }}>
        <h3>{title}</h3>
        <span className="faint">{t('app.empty')}</span>
      </div>
    );
  }
  return (
    <div className="stack" style={{ gap: 6 }}>
      <h3>{title}</h3>
      <div className="levels">
        {plan.take_profit.map((tp, i) => (
          <div className="level level--tp" key={`tp-${i}`}>
            <span>TP{i + 1} · {formatPrice(tp.price)}</span>
            <span className="muted">{tp.close_pct}%</span>
          </div>
        ))}
        {plan.stop_loss.map((sl, i) => (
          <div className="level level--sl" key={`sl-${i}`}>
            <span>SL{i + 1} · {formatPrice(sl.price)}</span>
            <span className="muted">{sl.close_pct}%</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function PositionActionModal({
  view,
  action,
  onClose,
  onDone,
}: {
  view: PositionView;
  action: Exclude<Action, null>;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState({
    execution_price: String(view.current_price ?? view.position.entry_price),
    close_pct: '25',
    quantity: '',
    fee_type: view.position.fee_type,
    actual_fee: '',
    amount: '',
    executed_at: toLocalInput(new Date()),
    note: '',
    take_profit: view.position.current_plan?.take_profit ?? [],
    stop_loss: view.position.current_plan?.stop_loss ?? [],
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const update = (key: string, value: unknown) => setForm((prev) => ({ ...prev, [key]: value }));

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const id = view.position.id;
      const at = new Date(form.executed_at).toISOString();

      switch (action) {
        case 'close':
          await api.closePosition(id, {
            execution_price: form.execution_price,
            fee_type: form.fee_type,
            actual_fee: form.actual_fee || null,
            executed_at: at,
            note: form.note,
          });
          break;
        case 'partial':
          await api.partialClose(id, {
            execution_price: form.execution_price,
            close_pct: form.quantity ? null : form.close_pct,
            quantity: form.quantity || null,
            fee_type: form.fee_type,
            actual_fee: form.actual_fee || null,
            executed_at: at,
            note: form.note,
          });
          break;
        case 'plan':
          await api.updatePlan(id, {
            take_profit: form.take_profit,
            stop_loss: form.stop_loss,
            note: form.note,
          });
          break;
        case 'fee':
          await api.addFee(id, {
            amount: form.amount,
            fee_type: form.fee_type,
            occurred_at: at,
            note: form.note,
          });
          break;
        case 'funding':
          await api.addFunding(id, { amount: form.amount, occurred_at: at, note: form.note });
          break;
      }
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const titles: Record<Exclude<Action, null>, string> = {
    close: t('positions.closeFull'),
    partial: t('positions.closePartial'),
    plan: t('positions.editPlan'),
    fee: t('positions.addFee'),
    funding: t('positions.addFunding'),
  };

  return (
    <Modal title={titles[action]} onClose={onClose}>
      {(action === 'close' || action === 'partial') && (
        <>
          <label className="field">
            <span className="field__label">{t('positions.executionPrice')}</span>
            <input
              value={form.execution_price}
              onChange={(e) => update('execution_price', e.target.value)}
            />
          </label>
          {action === 'partial' && (
            <>
              <div className="row">
                {['25', '50', '75', '100'].map((pct) => (
                  <button
                    key={pct}
                    className={form.close_pct === pct ? 'small primary' : 'small'}
                    onClick={() => {
                      update('close_pct', pct);
                      update('quantity', '');
                    }}
                  >
                    {pct}%
                  </button>
                ))}
              </div>
              <label className="field">
                <span className="field__label">{t('positions.quantity')}</span>
                <input
                  value={form.quantity}
                  onChange={(e) => update('quantity', e.target.value)}
                  placeholder={t('app.unknown')}
                />
              </label>
            </>
          )}
          <div className="grid grid--2">
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
          </div>
        </>
      )}

      {(action === 'fee' || action === 'funding') && (
        <>
          <label className="field">
            <span className="field__label">{t('positions.amount')}</span>
            <input value={form.amount} onChange={(e) => update('amount', e.target.value)} />
          </label>
          {action === 'funding' && <p className="faint">{t('positions.fundingHint')}</p>}
        </>
      )}

      {action === 'plan' && (
        <PlanEditor
          takeProfit={form.take_profit}
          stopLoss={form.stop_loss}
          onChange={(tp, sl) => {
            update('take_profit', tp);
            update('stop_loss', sl);
          }}
        />
      )}

      {action !== 'plan' && (
        <label className="field">
          <span className="field__label">{t('positions.executedAt')}</span>
          <input
            type="datetime-local"
            value={form.executed_at}
            onChange={(e) => update('executed_at', e.target.value)}
          />
        </label>
      )}

      <label className="field">
        <span className="field__label">{t('positions.note')}</span>
        <input value={form.note} onChange={(e) => update('note', e.target.value)} />
      </label>

      {error && <div className="banner banner--error">{error}</div>}

      <div className="row row--between">
        <button className="ghost" onClick={onClose}>
          {t('app.cancel')}
        </button>
        <button className="primary" disabled={busy} onClick={() => void submit()}>
          {t('app.confirm')}
        </button>
      </div>
    </Modal>
  );
}

function PlanEditor({
  takeProfit,
  stopLoss,
  onChange,
}: {
  takeProfit: PriceTarget[];
  stopLoss: PriceTarget[];
  onChange: (tp: PriceTarget[], sl: PriceTarget[]) => void;
}) {
  const { t } = useTranslation();

  const editGroup = (group: PriceTarget[], index: number, field: 'price' | 'close_pct', value: string) => {
    const next = group.map((item, i) => (i === index ? { ...item, [field]: Number(value) } : item));
    return next;
  };

  return (
    <div className="stack">
      <div className="stack" style={{ gap: 6 }}>
        <div className="row row--between">
          <h3>{t('recommendation.takeProfit')}</h3>
          <button
            className="small"
            onClick={() => onChange([...takeProfit, { price: 0, close_pct: 0 }], stopLoss)}
          >
            {t('app.add')}
          </button>
        </div>
        {takeProfit.map((tp, i) => (
          <div className="row" key={`tp-${i}`}>
            <input
              value={tp.price}
              onChange={(e) => onChange(editGroup(takeProfit, i, 'price', e.target.value), stopLoss)}
            />
            <input
              value={tp.close_pct}
              onChange={(e) => onChange(editGroup(takeProfit, i, 'close_pct', e.target.value), stopLoss)}
            />
            <button
              className="ghost danger"
              onClick={() => onChange(takeProfit.filter((_, idx) => idx !== i), stopLoss)}
            >
              ✕
            </button>
          </div>
        ))}
      </div>

      <div className="stack" style={{ gap: 6 }}>
        <div className="row row--between">
          <h3>{t('recommendation.stopLoss')}</h3>
          <button
            className="small"
            onClick={() => onChange(takeProfit, [...stopLoss, { price: 0, close_pct: 0 }])}
          >
            {t('app.add')}
          </button>
        </div>
        {stopLoss.map((sl, i) => (
          <div className="row" key={`sl-${i}`}>
            <input
              value={sl.price}
              onChange={(e) => onChange(takeProfit, editGroup(stopLoss, i, 'price', e.target.value))}
            />
            <input
              value={sl.close_pct}
              onChange={(e) => onChange(takeProfit, editGroup(stopLoss, i, 'close_pct', e.target.value))}
            />
            <button
              className="ghost danger"
              onClick={() => onChange(takeProfit, stopLoss.filter((_, idx) => idx !== i))}
            >
              ✕
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
