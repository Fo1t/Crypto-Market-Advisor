import { useTranslation } from 'react-i18next';

import { LANGUAGE_LABELS, SUPPORTED_LANGUAGES, setLanguage, type Language } from '../i18n';

/** Inline vectors render consistently even when the OS has no colour emoji font. */
function FlagIcon({ language }: { language: Language }) {
  if (language === 'ru') {
    return (
      <svg className="lang__flag" viewBox="0 0 60 40" aria-hidden="true">
        <rect width="60" height="40" fill="#fff" />
        <rect y="13.33" width="60" height="13.34" fill="#0039a6" />
        <rect y="26.67" width="60" height="13.33" fill="#d52b1e" />
      </svg>
    );
  }
  if (language === 'en') {
    return (
      <svg className="lang__flag" viewBox="0 0 60 40" aria-hidden="true">
        <rect width="60" height="40" fill="#012169" />
        <path d="M0 0 60 40M60 0 0 40" stroke="#fff" strokeWidth="9" />
        <path d="M0 0 60 40M60 0 0 40" stroke="#c8102e" strokeWidth="4" />
        <path d="M30 0v40M0 20h60" stroke="#fff" strokeWidth="13" />
        <path d="M30 0v40M0 20h60" stroke="#c8102e" strokeWidth="7" />
      </svg>
    );
  }
  return (
    <svg className="lang__flag" viewBox="0 0 60 40" aria-hidden="true">
      <rect width="60" height="40" fill="#de2910" />
      <path d="m12 5 2.2 6.6h7L15.5 15l2.2 6.6-5.7-4.1-5.7 4.1 2.2-6.6-5.7-3.4h7z" fill="#ffde00" />
    </svg>
  );
}

/**
 * LanguageSwitcher renders one button per supported language.
 * The active language is marked with aria-pressed so screen readers and the
 * visual state agree, and the flag alone is never the only cue — each button
 * carries the language name as its accessible label and tooltip.
 */
export function LanguageSwitcher({
  compact = false,
  onChange,
  value,
}: {
  compact?: boolean;
  onChange?: (language: Language) => void;
  value?: Language;
}) {
  const { t, i18n } = useTranslation();
  const current = (value ?? (i18n.language as Language)) || 'ru';

  const select = (language: Language) => {
    if (onChange) {
      onChange(language);
      return;
    }
    setLanguage(language);
  };

  return (
    <div className="lang" role="group" aria-label={t('settings.language')}>
      {SUPPORTED_LANGUAGES.map((language) => (
        <button
          key={language}
          type="button"
          className={language === current ? 'lang__btn lang__btn--active' : 'lang__btn'}
          aria-pressed={language === current}
          aria-label={LANGUAGE_LABELS[language]}
          title={LANGUAGE_LABELS[language]}
          onClick={() => select(language)}
        >
          <FlagIcon language={language} />
          {!compact && <span className="lang__name">{LANGUAGE_LABELS[language]}</span>}
        </button>
      ))}
    </div>
  );
}
