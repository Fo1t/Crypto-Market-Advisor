import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import type { NewsCluster, NewsSource } from '../api/types';
import { AsyncBoundary, Badge, Card, Modal, Stat } from '../components/common';
import { NewsEventCard } from '../components/NewsEventCard';
import { useApi } from '../hooks/useApi';
import { formatDateTime, formatNumber, formatPrice } from '../utils/format';

const CATEGORIES = ['market', 'regulation', 'legal', 'security', 'exploit', 'hack', 'exchange', 'listing', 'delisting', 'trading_suspension', 'protocol', 'network_upgrade', 'network_outage', 'etf', 'institutional', 'macro', 'mining', 'stablecoin', 'defi', 'tokenomics', 'partnership', 'other'];
const PAGE_SIZE = 20;

export function NewsPage() {
  const { t, i18n } = useTranslation();
  const [params, setParams] = useSearchParams();
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [sourceFormOpen, setSourceFormOpen] = useState(false);
  const [sourceDraft, setSourceDraft] = useState({ name: '', url: '', provider: 'rss' as 'rss' | 'atom', priority: 50 });
  const [sourceBusy, setSourceBusy] = useState(false);
  const [sourceError, setSourceError] = useState<string | null>(null);
  const [confirmDisable, setConfirmDisable] = useState<NewsSource | null>(null);

  const filters = useMemo(() => ({
    q: params.get('q') ?? '',
    asset: params.get('asset') ?? '',
    category: params.get('category') ?? '',
    critical: params.get('critical') === 'true' ? true : undefined,
    min_importance: params.get('min_importance') ? Number(params.get('min_importance')) : undefined,
    sort: params.get('sort') ?? 'latest',
    days: params.get('days') ? Number(params.get('days')) : 30,
    limit: PAGE_SIZE,
    offset: params.get('offset') ? Number(params.get('offset')) : 0,
  }), [params]);

  const list = useApi(() => api.news(filters), [params.toString()], 60_000);
  const stats = useApi(() => api.newsStats(), [], 60_000);
  const sources = useApi(() => api.newsSources(), [], 60_000);
  const detail = useApi(() => selectedID ? api.newsItem(selectedID) : Promise.resolve(null), [selectedID]);

  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value); else next.delete(key);
    if (key !== 'offset') next.delete('offset');
    setParams(next, { replace: true });
  };

  const saveSource = async () => {
    setSourceBusy(true);
    setSourceError(null);
    try {
      await api.createNewsSource({ ...sourceDraft, enabled: true });
      setSourceDraft({ name: '', url: '', provider: 'rss', priority: 50 });
      setSourceFormOpen(false);
      sources.reload();
    } catch (error) {
      setSourceError(error instanceof Error ? error.message : t('news.sourceSaveFailed'));
    } finally {
      setSourceBusy(false);
    }
  };

  const toggleSource = async (source: NewsSource, enabled: boolean) => {
    setSourceBusy(true);
    setSourceError(null);
    try {
      if (enabled) await api.updateNewsSource(source.id, { enabled: true });
      else await api.disableNewsSource(source.id);
      sources.reload();
    } catch (error) {
      setSourceError(error instanceof Error ? error.message : t('news.sourceSaveFailed'));
    } finally {
      setSourceBusy(false);
      setConfirmDisable(null);
    }
  };

  const total = list.data?.total ?? 0;
  return (
    <>
      {stats.data && (
        <div className="grid grid--4">
          <Stat label={t('news.events')} value={stats.data.clusters_total} />
          <Stat label={t('news.criticalEvents')} value={stats.data.critical_total} tone={stats.data.critical_total ? 'warn' : undefined} />
          <Stat label={t('news.activeSources')} value={`${stats.data.sources_enabled}/${stats.data.sources_total}`} />
          <Stat label={t('news.lastUpdate')} value={formatDateTime(stats.data.last_seen_at, i18n.language)} />
        </div>
      )}

      <Card title={t('news.title')} actions={<button type="button" onClick={() => setSourceFormOpen(true)}>{t('news.addSource')}</button>}>
        <p className="faint">{t('news.originalLanguageHint')}</p>
        <div className="news-filters">
          <label className="field"><span className="field__label">{t('news.search')}</span><input value={filters.q} onChange={(e) => setFilter('q', e.target.value)} placeholder={t('news.searchPlaceholder')} /></label>
          <label className="field"><span className="field__label">{t('news.asset')}</span><input value={filters.asset} onChange={(e) => setFilter('asset', e.target.value.toUpperCase())} placeholder="BTC" /></label>
          <label className="field"><span className="field__label">{t('news.category')}</span><select value={filters.category} onChange={(e) => setFilter('category', e.target.value)}><option value="">{t('app.all')}</option>{CATEGORIES.map((category) => <option key={category} value={category}>{t(`news.categories.${category}`)}</option>)}</select></label>
          <label className="field"><span className="field__label">{t('news.importance')}</span><select value={filters.min_importance ?? ''} onChange={(e) => setFilter('min_importance', e.target.value)}><option value="">{t('app.all')}</option><option value="0.5">≥ 50%</option><option value="0.7">≥ 70%</option><option value="0.9">≥ 90%</option></select></label>
          <label className="field"><span className="field__label">{t('news.sort')}</span><select value={filters.sort} onChange={(e) => setFilter('sort', e.target.value)}><option value="latest">{t('news.latest')}</option><option value="importance">{t('news.mostImportant')}</option></select></label>
          <label className="field"><span className="field__label">{t('news.period')}</span><select value={filters.days} onChange={(e) => setFilter('days', e.target.value)}><option value="1">24h</option><option value="7">7d</option><option value="30">30d</option><option value="90">90d</option></select></label>
          <label className="field field--inline news-filters__check"><input type="checkbox" checked={filters.critical === true} onChange={(e) => setFilter('critical', e.target.checked ? 'true' : '')} /><span>{t('news.onlyCritical')}</span></label>
        </div>
      </Card>

      {stats.error && <div className="banner banner--warn">{t('news.statsUnavailable')}</div>}
      <AsyncBoundary loading={list.loading} error={list.error} onRetry={list.reload} hasData={!!list.data}>
        <div className="stack">
          {list.data?.items.map((event) => <NewsEventCard key={event.id} event={event} onOpen={() => setSelectedID(event.id)} />)}
          {list.data?.items.length === 0 && <div className="banner">{t('news.empty')}</div>}
        </div>
      </AsyncBoundary>
      {total > PAGE_SIZE && <div className="row row--between"><button type="button" disabled={filters.offset === 0} onClick={() => setFilter('offset', String(Math.max(0, filters.offset - PAGE_SIZE)))}>{t('news.previous')}</button><span className="faint">{filters.offset + 1}–{Math.min(filters.offset + PAGE_SIZE, total)} / {total}</span><button type="button" disabled={filters.offset + PAGE_SIZE >= total} onClick={() => setFilter('offset', String(filters.offset + PAGE_SIZE))}>{t('news.next')}</button></div>}

      <Card title={t('news.sources')}>
        {sourceError && <div className="banner banner--error">{sourceError}</div>}
        <AsyncBoundary loading={sources.loading} error={sources.error} onRetry={sources.reload} hasData={!!sources.data}>
          <div className="table-wrap"><table><thead><tr><th>{t('news.source')}</th><th>{t('news.provider')}</th><th>{t('news.sourceStatus')}</th><th>{t('news.lastSuccess')}</th><th>{t('news.actions')}</th></tr></thead><tbody>{sources.data?.map((source) => <tr key={source.id}><td><div>{source.name}</div><div className="faint news-source-url">{source.url}</div></td><td>{source.provider.toUpperCase()}</td><td><Badge tone={source.status === 'online' ? 'long' : source.status === 'degraded' ? 'warn' : source.status === 'offline' ? 'short' : undefined}>{t(`news.sourceStatuses.${source.status}`)}</Badge>{source.last_error && <div className="faint warn">{source.last_error}</div>}</td><td>{formatDateTime(source.last_success_at, i18n.language)}</td><td>{source.enabled ? <button className="small danger" type="button" disabled={sourceBusy} onClick={() => setConfirmDisable(source)}>{t('news.disable')}</button> : <button className="small" type="button" disabled={sourceBusy} onClick={() => void toggleSource(source, true)}>{t('news.enable')}</button>}</td></tr>)}</tbody></table></div>
        </AsyncBoundary>
      </Card>

      {selectedID && <Modal title={t('news.details')} onClose={() => setSelectedID(null)}><AsyncBoundary loading={detail.loading} error={detail.error} onRetry={detail.reload} hasData={!!detail.data}>{detail.data && <NewsDetail event={detail.data} />}</AsyncBoundary></Modal>}
      {sourceFormOpen && <Modal title={t('news.addSource')} onClose={() => setSourceFormOpen(false)}><label className="field"><span className="field__label">{t('news.sourceName')}</span><input value={sourceDraft.name} onChange={(e) => setSourceDraft((v) => ({ ...v, name: e.target.value }))} /></label><label className="field"><span className="field__label">URL</span><input type="url" value={sourceDraft.url} onChange={(e) => setSourceDraft((v) => ({ ...v, url: e.target.value }))} placeholder="https://example.com/feed.xml" /></label><label className="field"><span className="field__label">{t('news.provider')}</span><select value={sourceDraft.provider} onChange={(e) => setSourceDraft((v) => ({ ...v, provider: e.target.value as 'rss' | 'atom' }))}><option value="rss">RSS</option><option value="atom">Atom</option></select></label><label className="field"><span className="field__label">{t('news.priority')}</span><input type="number" min="0" max="100" value={sourceDraft.priority} onChange={(e) => setSourceDraft((v) => ({ ...v, priority: Number(e.target.value) }))} /></label>{sourceError && <div className="banner banner--error">{sourceError}</div>}<div className="row"><button className="primary" type="button" disabled={sourceBusy || !sourceDraft.name || !sourceDraft.url} onClick={() => void saveSource()}>{t('app.save')}</button><button type="button" onClick={() => setSourceFormOpen(false)}>{t('app.cancel')}</button></div></Modal>}
      {confirmDisable && <Modal title={t('news.disableSource')} onClose={() => setConfirmDisable(null)}><p>{t('news.disableConfirm', { name: confirmDisable.name })}</p><div className="row"><button className="danger" type="button" disabled={sourceBusy} onClick={() => void toggleSource(confirmDisable, false)}>{t('news.disable')}</button><button type="button" onClick={() => setConfirmDisable(null)}>{t('app.cancel')}</button></div></Modal>}
    </>
  );
}

function NewsDetail({ event }: { event: NewsCluster }) {
  const { t } = useTranslation();
  return <div className="stack"><NewsEventCard event={event} compact />{event.reactions.length > 0 && <div><h3>{t('news.marketReaction')}</h3><div className="table-wrap"><table><thead><tr><th>{t('news.asset')}</th><th>5m</th><th>15m</th><th>1h</th><th>4h</th><th>24h</th><th>MFE / MAE</th></tr></thead><tbody>{event.reactions.map((reaction) => <tr key={reaction.asset_id}><td>{reaction.symbol}</td>{[reaction.return_5m_pct, reaction.return_15m_pct, reaction.return_1h_pct, reaction.return_4h_pct, reaction.return_24h_pct].map((value, index) => <td key={index} className={value === undefined ? 'numeric muted' : value >= 0 ? 'numeric long' : 'numeric short'}>{value === undefined ? '—' : `${formatNumber(value, 2)}%`}</td>)}<td className="numeric">{reaction.max_up_move_pct === undefined ? '—' : `${formatNumber(reaction.max_up_move_pct, 2)}%`} / {reaction.max_down_move_pct === undefined ? '—' : `${formatNumber(reaction.max_down_move_pct, 2)}%`}</td></tr>)}</tbody></table></div></div>}{event.publications?.length ? <div><h3>{t('news.publications')}</h3><div className="stack">{event.publications.map((publication) => <a key={publication.id} className="news-publication" href={publication.url} target="_blank" rel="noreferrer"><strong>{publication.source.name}</strong><span>{publication.title}</span></a>)}</div></div> : null}{event.reactions.some((reaction) => reaction.baseline_price) && <span className="faint">{t('news.baseline')}: {event.reactions.map((reaction) => `${reaction.symbol} ${formatPrice(reaction.baseline_price)}`).join(' · ')}</span>}</div>;
}
