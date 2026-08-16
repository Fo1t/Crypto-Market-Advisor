import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { LanguageSwitcher } from './LanguageSwitcher';

export const RISK_DISCLOSURE_STORAGE_KEY = 'cma.riskDisclosure';
export const RISK_DISCLOSURE_VERSION = 1;

function disclosureAccepted(): boolean {
  try {
    const saved = window.localStorage.getItem(RISK_DISCLOSURE_STORAGE_KEY);
    if (!saved) return false;
    const parsed = JSON.parse(saved) as { version?: unknown };
    return parsed.version === RISK_DISCLOSURE_VERSION;
  } catch {
    return false;
  }
}

export function RiskDisclosureModal() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(() => !disclosureAccepted());
  const [confirmed, setConfirmed] = useState(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    if (!open) return undefined;

    const previousOverflow = document.body.style.overflow;
    const application = document.querySelector<HTMLElement>('.app');
    const previousAriaHidden = application?.getAttribute('aria-hidden');
    document.body.style.overflow = 'hidden';
    application?.setAttribute('inert', '');
    application?.setAttribute('aria-hidden', 'true');
    titleRef.current?.focus();

    const keepFocusInside = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        return;
      }
      if (event.key !== 'Tab' || !dialogRef.current) return;

      const focusable = Array.from(
        dialogRef.current.querySelectorAll<HTMLElement>(
          'button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
        ),
      );
      if (focusable.length === 0) {
        event.preventDefault();
        return;
      }

      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', keepFocusInside);
    return () => {
      document.body.style.overflow = previousOverflow;
      application?.removeAttribute('inert');
      if (previousAriaHidden === null || previousAriaHidden === undefined) {
        application?.removeAttribute('aria-hidden');
      } else {
        application?.setAttribute('aria-hidden', previousAriaHidden);
      }
      document.removeEventListener('keydown', keepFocusInside);
    };
  }, [open]);

  if (!open) return null;

  const accept = () => {
    if (!confirmed) return;
    try {
      window.localStorage.setItem(
        RISK_DISCLOSURE_STORAGE_KEY,
        JSON.stringify({ version: RISK_DISCLOSURE_VERSION, acceptedAt: new Date().toISOString() }),
      );
    } catch {
      // The current session may continue even when browser storage is unavailable.
    }
    setOpen(false);
  };

  return (
    <div className="modal-backdrop risk-disclosure-backdrop">
      <div
        ref={dialogRef}
        className="modal risk-disclosure"
        role="dialog"
        aria-modal="true"
        aria-labelledby="risk-disclosure-title"
        aria-describedby="risk-disclosure-intro"
      >
        <header className="risk-disclosure__header">
          <span className="risk-disclosure__icon" aria-hidden="true">!</span>
          <div>
            <p className="risk-disclosure__eyebrow">{t('riskDisclosure.eyebrow')}</p>
            <h1 id="risk-disclosure-title" ref={titleRef} tabIndex={-1}>
              {t('riskDisclosure.title')}
            </h1>
          </div>
        </header>

        <LanguageSwitcher />

        <p id="risk-disclosure-intro" className="risk-disclosure__intro">
          {t('riskDisclosure.intro')}
        </p>

        <div className="risk-disclosure__section">
          <h2>{t('riskDisclosure.informationOnlyTitle')}</h2>
          <p>{t('riskDisclosure.informationOnlyText')}</p>
        </div>

        <div className="risk-disclosure__section">
          <h2>{t('riskDisclosure.risksTitle')}</h2>
          <ul>
            <li>{t('riskDisclosure.riskVolatility')}</li>
            <li>{t('riskDisclosure.riskData')}</li>
            <li>{t('riskDisclosure.riskHistory')}</li>
            <li>{t('riskDisclosure.riskExecution')}</li>
          </ul>
        </div>

        <div className="risk-disclosure__section">
          <h2>{t('riskDisclosure.responsibilityTitle')}</h2>
          <p>{t('riskDisclosure.responsibilityText')}</p>
        </div>

        <div className="risk-disclosure__section risk-disclosure__section--warning">
          <h2>{t('riskDisclosure.liabilityTitle')}</h2>
          <p>{t('riskDisclosure.liabilityText')}</p>
        </div>

        <label className="risk-disclosure__confirmation">
          <input
            type="checkbox"
            checked={confirmed}
            onChange={(event) => setConfirmed(event.target.checked)}
          />
          <span>{t('riskDisclosure.confirmation')}</span>
        </label>

        <div className="risk-disclosure__footer">
          <p>{t('riskDisclosure.storageNote')}</p>
          <button type="button" className="primary" disabled={!confirmed} onClick={accept}>
            {t('riskDisclosure.continue')}
          </button>
        </div>
      </div>
    </div>
  );
}
