import { useTranslation } from 'react-i18next';

import type { StrategyDecision, StrategyVote } from '../api/types';
import { formatNumber } from '../utils/format';
import { Badge, Card, Stat } from './common';

/** voteTone colours a vote by the side it argues for. */
function voteTone(vote: StrategyVote): 'long' | 'short' | undefined {
  if (vote.kind === 'filter') return 'short';
  if (vote.direction === 'bullish') return 'long';
  if (vote.direction === 'bearish') return 'short';
  return undefined;
}

/**
 * StrategyDecisionCard shows the deterministic verdict: which strategies voted,
 * how much weight each carried and why the engine did or did not open anything.
 * It is produced without the LLM, so it stays meaningful while the model is off.
 */
export function StrategyDecisionCard({
  decision,
  inset = false,
}: {
  decision: StrategyDecision;
  inset?: boolean;
}) {
  const { t } = useTranslation();
  const entry = decision.action === 'OPEN_LONG' || decision.action === 'OPEN_SHORT';
  const votes = decision.votes ?? [];
  const maxScore = votes.reduce((acc, vote) => Math.max(acc, Math.abs(vote.score)), 0);

  return (
    <Card
      title={t('strategyDecision.title')}
      inset={inset}
      actions={
        <Badge tone={entry ? (decision.action === 'OPEN_LONG' ? 'long' : 'short') : 'warn'}>
          {t(`enums.action.${decision.action}`, { defaultValue: decision.action })}
        </Badge>
      }
    >
      <p className="faint short">
        {t(`strategyDecision.reasons.${decision.reason}`, { defaultValue: decision.reason })}
        {decision.timeframe ? ` · ${decision.timeframe}` : ''}
      </p>

      <div className="grid grid--4" style={{ marginTop: 8 }}>
        <Stat
          label={t('strategyDecision.net')}
          value={formatNumber(decision.net_score, 2)}
          tone={decision.net_score >= 0 ? 'long' : 'short'}
        />
        <Stat label={t('strategyDecision.block')} value={formatNumber(decision.block_score, 2)} />
        <Stat label={t('strategyDecision.minSignal')} value={formatNumber(decision.min_signal, 2)} />
        <Stat label={t('recommendation.confidence')} value={entry ? `${decision.confidence}%` : '—'} />
      </div>

      {votes.length === 0 ? (
        <p className="faint short" style={{ marginTop: 10 }}>
          {t('strategyDecision.noVotes')}
        </p>
      ) : (
        <div className="breakdown" style={{ marginTop: 10 }}>
          {votes.map((vote) => (
            <div key={vote.id} className="breakdown__row">
              <span className="breakdown__label">
                {t(`strategies.${vote.id}`, { defaultValue: vote.id })}
                {vote.hard_veto && <span className="short"> · {t('settings.hardVeto')}</span>}
              </span>
              <span className="breakdown__track">
                <span
                  className={`breakdown__bar${voteTone(vote) ? ` breakdown__bar--${voteTone(vote)}` : ''}`}
                  style={{ width: `${maxScore > 0 ? Math.max(2, (Math.abs(vote.score) / maxScore) * 100) : 2}%` }}
                />
              </span>
              <span className="breakdown__value numeric" title={vote.detail ?? ''}>
                {formatNumber(vote.score, 2)}
                <span className="faint"> ×{formatNumber(vote.weight, 1)}</span>
              </span>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}
