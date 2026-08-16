import { useState } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

/** Card is the standard content container. */
export function Card({
  title,
  actions,
  children,
  inset = false,
  collapsible = false,
  defaultOpen = true,
  storageKey,
}: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  inset?: boolean;
  /** Turns the card into a section that folds away. Use it for anything long
   * enough that the sections after it stop being reachable by eye. */
  collapsible?: boolean;
  defaultOpen?: boolean;
  /** Remembers the open state between visits. Without it a card the user opened
   * closes again on every reload, which is worse than not folding at all. */
  storageKey?: string;
}) {
  const className = inset ? 'card card--inset' : 'card';

  if (!collapsible) {
    return (
      <section className={className}>
        {(title || actions) && (
          <header className="card__header">
            {typeof title === 'string' ? <h2>{title}</h2> : title}
            {actions}
          </header>
        )}
        {children}
      </section>
    );
  }

  return (
    <CollapsibleCard
      className={className}
      title={title}
      actions={actions}
      defaultOpen={defaultOpen}
      storageKey={storageKey}
    >
      {children}
    </CollapsibleCard>
  );
}

/**
 * CollapsibleCard is a section that folds away, for anything long enough that
 * the sections after it stop being reachable by eye.
 *
 * It uses the native disclosure element, which brings the keyboard behaviour,
 * the focus handling and the semantics already correct. The open state is held
 * in React rather than left to the DOM: the settings page re-renders on every
 * keystroke, and an uncontrolled element would snap back to its initial state
 * mid-edit.
 */
function CollapsibleCard({
  className,
  title,
  actions,
  children,
  defaultOpen,
  storageKey,
}: {
  className: string;
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  defaultOpen: boolean;
  storageKey?: string;
}) {
  const [open, setOpen] = useState(() => {
    if (!storageKey) return defaultOpen;
    const stored = window.localStorage.getItem(`card:${storageKey}`);
    return stored === null ? defaultOpen : stored === 'open';
  });

  const toggle = (next: boolean) => {
    setOpen(next);
    if (storageKey) {
      window.localStorage.setItem(`card:${storageKey}`, next ? 'open' : 'closed');
    }
  };

  return (
    <details
      className={`${className} card--collapsible`}
      open={open}
      onToggle={(event) => toggle(event.currentTarget.open)}
    >
      <summary className="card__header card__header--summary">
        {typeof title === 'string' ? <h2>{title}</h2> : title}
        {/* Actions live outside the summary click target: a button inside it
            would toggle the card as well as doing its own job. */}
      </summary>
      {actions && <div className="card__actions">{actions}</div>}
      {children}
    </details>
  );
}

/** Stat renders one labelled figure. */
export function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  tone?: 'long' | 'short' | 'warn';
}) {
  return (
    <div className="stat">
      <span className="stat__label">{label}</span>
      <span className={tone ? `stat__value ${tone}` : 'stat__value'}>{value}</span>
      {hint && <span className="stat__hint">{hint}</span>}
    </div>
  );
}

/** Badge is a compact status chip. */
export function Badge({
  children,
  tone,
}: {
  children: ReactNode;
  tone?: 'long' | 'short' | 'warn' | 'accent';
}) {
  return <span className={tone ? `badge badge--${tone}` : 'badge'}>{children}</span>;
}

/** AsyncBoundary renders loading and error states consistently. */
export function AsyncBoundary({
  loading,
  error,
  onRetry,
  children,
  hasData,
}: {
  loading: boolean;
  error: string | null;
  onRetry?: () => void;
  children: ReactNode;
  hasData: boolean;
}) {
  const { t } = useTranslation();

  if (error && !hasData) {
    return (
      <div className="banner banner--error">
        <div className="row row--between">
          <span>
            {t('app.error')}: {error}
          </span>
          {onRetry && (
            <button className="small" onClick={onRetry}>
              {t('app.retry')}
            </button>
          )}
        </div>
      </div>
    );
  }
  if (loading && !hasData) {
    return (
      <div className="row muted">
        <span className="spinner" /> {t('app.loading')}
      </div>
    );
  }
  return <>{children}</>;
}

/** Modal is a simple centred dialog. */
export function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <div className="modal-backdrop" onClick={onClose} role="presentation">
      <div className="modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true" aria-label={title}>
        <div className="row row--between">
          <h2>{title}</h2>
          <button className="ghost" onClick={onClose} aria-label={t('app.close')}>
            ✕
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

/** Bar renders a labelled proportional bar. */
export function Bar({
  label,
  value,
  max = 1,
  tone,
  display,
}: {
  label: string;
  value: number;
  max?: number;
  tone?: 'long' | 'short';
  display?: string;
}) {
  const pct = Math.max(0, Math.min(100, (Math.abs(value) / max) * 100));
  return (
    <div className="bar-row">
      <span className="muted">{label}</span>
      <div className="bar">
        <div className={tone ? `bar__fill bar__fill--${tone}` : 'bar__fill'} style={{ width: `${pct}%` }} />
      </div>
      <span className="mono">{display ?? value.toFixed(2)}</span>
    </div>
  );
}
