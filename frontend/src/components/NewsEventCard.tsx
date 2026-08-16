import { useTranslation } from 'react-i18next';

import type { NewsCluster } from '../api/types';
import { formatDateTime, formatNumber } from '../utils/format';
import { Badge } from './common';

export function NewsEventCard({ event, onOpen, compact = false }: { event: NewsCluster; onOpen?: () => void; compact?: boolean }) {
  const { t, i18n } = useTranslation();
  return (
    <article className={event.critical ? 'news-event news-event--critical' : 'news-event'}>
      <div className="news-event__meta">
        <div className="row">
          {event.critical && <Badge tone="short">{t('news.critical')}</Badge>}
          {event.categories.slice(0, compact ? 2 : 4).map(({ category }) => (
            <Badge key={category}>{t(`news.categories.${category}`)}</Badge>
          ))}
          {event.assets.map((asset) => <Badge key={asset.id} tone="accent">{asset.symbol}</Badge>)}
        </div>
        <time dateTime={event.first_published_at}>{formatDateTime(event.first_published_at, i18n.language)}</time>
      </div>
      <h3>{event.canonical_title}</h3>
      {!compact && event.canonical_summary && <p className="news-event__summary">{event.canonical_summary}</p>}
      <div className="news-event__footer">
        <span>{event.sources.map((source) => source.name).join(', ')}</span>
        <span>{t('news.importance')}: {formatNumber(event.importance * 100, 0)}%</span>
        <span>{t('news.sourcesCount', { count: event.source_count })}</span>
        {onOpen && <button type="button" className="small" onClick={onOpen}>{t('news.details')}</button>}
      </div>
    </article>
  );
}
