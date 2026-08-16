import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api } from '../api/client';
import { useApi } from '../hooks/useApi';
import { AsyncBoundary, Card } from '../components/common';
import { PositionCard } from '../components/PositionCard';
import { OpenPositionModal } from '../components/OpenPositionModal';

export function PositionsPage() {
  const { t } = useTranslation();
  const [onlyOpen, setOnlyOpen] = useState(true);
  const [showCreate, setShowCreate] = useState(false);

  const { data, loading, error, reload } = useApi(() => api.positions(onlyOpen), [onlyOpen], 30_000);

  return (
    <>
      <Card
        title={t('positions.title')}
        actions={
          <div className="row">
            <div className="tabs">
              <button className={onlyOpen ? 'tab tab--active' : 'tab'} onClick={() => setOnlyOpen(true)}>
                {t('positions.open')}
              </button>
              <button className={!onlyOpen ? 'tab tab--active' : 'tab'} onClick={() => setOnlyOpen(false)}>
                {t('app.all')}
              </button>
            </div>
            <button className="small primary" onClick={() => setShowCreate(true)}>
              {t('positions.recordTrade')}
            </button>
          </div>
        }
      >
        <AsyncBoundary loading={loading} error={error} onRetry={reload} hasData={!!data}>
          <div className="stack">
            {data?.items.map((view) => (
              <PositionCard key={view.position.id} view={view} onChanged={reload} />
            ))}
            {data && data.items.length === 0 && <span className="faint">{t('positions.noPositions')}</span>}
          </div>
        </AsyncBoundary>
      </Card>

      {showCreate && (
        <OpenPositionModal
          onClose={() => setShowCreate(false)}
          onCreated={() => {
            setShowCreate(false);
            reload();
          }}
        />
      )}
    </>
  );
}
