import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import { TIMEFRAMES, type ImportProgress, type Timeframe } from '../api/types';
import { useApi } from '../hooks/useApi';

/** How often a running job is re-read. Slow enough to be free, fast enough to feel live. */
const POLL_MS = 2000;

function isoDay(date: Date): string {
  return date.toISOString().slice(0, 10);
}

/**
 * HistoryImport downloads an explicit window of candles for the assets the user
 * picks.
 *
 * The scheduled backfill keeps a fixed window per timeframe and only ever moves
 * forward from the newest stored bar, which is right for staying current and
 * useless for the two things people actually want: a longer past than the
 * automatic window reaches, and a repair of a period that was fetched while the
 * exchange was returning gaps. Both are the same operation - name a period, ask
 * for it again - so this is one form rather than two buttons.
 *
 * The job itself runs in the backend. This only starts it and watches it, so
 * closing the tab does not abandon a download.
 */
export function HistoryImport() {
  const { t } = useTranslation();
  const markets = useApi(() => api.markets(), [], 0);
  const [symbols, setSymbols] = useState<string[]>([]);
  const [timeframes, setTimeframes] = useState<Timeframe[]>(['1h', '4h', '1d']);
  const [from, setFrom] = useState(() => {
    const year = new Date();
    year.setFullYear(year.getFullYear() - 1);
    return isoDay(year);
  });
  const [to, setTo] = useState(() => isoDay(new Date()));
  const [job, setJob] = useState<ImportProgress | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const running = job?.status === 'running';

  // A job survives a page reload, so the screen has to find one that is already
  // in flight instead of pretending nothing is happening.
  useEffect(() => {
    api
      .importStatus()
      .then((status) => {
        if (status.status !== 'idle') setJob(status);
      })
      .catch(() => {
        // A missing status is not worth an error banner; the form still works.
      });
  }, []);

  useEffect(() => {
    if (!running) return undefined;
    const timer = window.setInterval(() => {
      api.importStatus().then(setJob).catch(() => {});
    }, POLL_MS);
    return () => window.clearInterval(timer);
  }, [running]);

  const toggle = <T extends string>(list: T[], value: T): T[] =>
    list.includes(value) ? list.filter((v) => v !== value) : [...list, value];

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      setJob(await api.importHistory({ symbols, timeframes, from, to }));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    setBusy(true);
    try {
      setJob(await api.cancelImport());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const assets = markets.data?.items ?? [];
  const canStart = symbols.length > 0 && timeframes.length > 0 && !running && !busy;
  // A minute bar over a long window is hundreds of thousands of rows and the
  // slowest thing this screen can ask for. Saying so beats an unexplained wait.
  const heavy = timeframes.includes('1m') || timeframes.includes('5m');

  return (
    <>
      <p className="faint short">{t('settings.importHint')}</p>

      <div className="field">
        <span className="field__label">{t('settings.importAssets')}</span>
        <div className="row" style={{ marginBottom: 6 }}>
          <button className="small ghost" onClick={() => setSymbols(assets.map((a) => a.symbol))}>
            {t('settings.importSelectAll')}
          </button>
          <button className="small ghost" onClick={() => setSymbols([])}>
            {t('settings.importSelectNone')}
          </button>
          <span className="faint">{t('settings.importSelected', { count: symbols.length })}</span>
        </div>
        <div className="import-assets">
          {assets.map((asset) => (
            <label key={asset.symbol} className="field--inline row" style={{ gap: 5 }}>
              <input
                type="checkbox"
                checked={symbols.includes(asset.symbol)}
                onChange={() => setSymbols((prev) => toggle(prev, asset.symbol))}
              />
              <span>{asset.symbol}</span>
            </label>
          ))}
          {assets.length === 0 && <span className="faint">{t('settings.importNoAssets')}</span>}
        </div>
      </div>

      <div className="field" style={{ marginTop: 12 }}>
        <span className="field__label">{t('settings.timeframes')}</span>
        <div className="row">
          {TIMEFRAMES.map((tf) => (
            <label key={tf} className="field--inline row" style={{ gap: 5 }}>
              <input
                type="checkbox"
                checked={timeframes.includes(tf)}
                onChange={() => setTimeframes((prev) => toggle(prev, tf))}
              />
              <span>{tf}</span>
            </label>
          ))}
        </div>
        {heavy && <span className="faint short">{t('settings.importHeavyHint')}</span>}
      </div>

      <div className="grid grid--3" style={{ marginTop: 12 }}>
        <label className="field">
          <span className="field__label">{t('settings.importFrom')}</span>
          <input type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="field">
          <span className="field__label">{t('settings.importTo')}</span>
          <input type="date" value={to} min={from} max={isoDay(new Date())} onChange={(e) => setTo(e.target.value)} />
        </label>
      </div>

      {error && <div className="banner banner--error">{error}</div>}

      <div className="row" style={{ marginTop: 12 }}>
        <button className="primary" disabled={!canStart} onClick={() => void start()}>
          {t('settings.importStart')}
        </button>
        {running && (
          <button className="ghost" disabled={busy} onClick={() => void cancel()}>
            {t('settings.importCancel')}
          </button>
        )}
      </div>

      {job && job.status !== 'idle' && <ImportReport job={job} />}
    </>
  );
}

/** ImportReport shows the pace while a job runs and its result once it stops. */
function ImportReport({ job }: { job: ImportProgress }) {
  const { t } = useTranslation();
  const pct = job.total > 0 ? Math.round((job.completed / job.total) * 100) : 0;
  const active = job.status === 'running';
  const failed = job.items.filter((item) => item.error);

  return (
    <div className="stack" style={{ marginTop: 12 }}>
      <div className="progress">
        <div className="progress__track">
          <div
            className={`progress__bar${active ? ' progress__bar--active' : ''}`}
            style={{ width: `${pct}%` }}
          />
        </div>
        <span className="faint short">
          {t(`settings.importStatus.${job.status}`)} · {job.completed}/{job.total}
          {active && job.current ? ` · ${job.current}` : ''}
        </span>
      </div>

      <span className="short">{t('settings.importStored', { count: job.candles })}</span>
      {job.error && <div className="banner banner--error">{job.error}</div>}

      {failed.length > 0 && (
        <div className="stack">
          <span className="faint short">{t('settings.importFailures')}</span>
          {failed.map((item) => (
            <span key={`${item.symbol}-${item.timeframe}`} className="faint short">
              {item.symbol} {item.timeframe}: {item.error}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
