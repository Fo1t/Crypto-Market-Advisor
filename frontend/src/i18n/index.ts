import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';

import { ru } from './locales/ru';
import { en } from './locales/en';
import { zhCN } from './locales/zh-CN';

export const SUPPORTED_LANGUAGES = ['en', 'ru', 'zh-CN'] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];

export const LANGUAGE_LABELS: Record<Language, string> = {
  ru: 'Русский',
  en: 'English',
  'zh-CN': '简体中文',
};

const STORAGE_KEY = 'cma.language';

/** Reads the persisted language, falling back to English as the default. */
export function storedLanguage(): Language {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY);
    if (saved && (SUPPORTED_LANGUAGES as readonly string[]).includes(saved)) {
      return saved as Language;
    }
  } catch {
    // localStorage can be unavailable in private mode; the default still works.
  }
  return 'en';
}

/** Switches the language at runtime and persists the choice. */
export function setLanguage(language: Language): void {
  void i18n.changeLanguage(language);
  document.documentElement.lang = language;
  try {
    window.localStorage.setItem(STORAGE_KEY, language);
  } catch {
    // Persistence is a convenience, not a requirement.
  }
}

const initialLanguage = storedLanguage();
document.documentElement.lang = initialLanguage;

void i18n.use(initReactI18next).init({
  resources: {
    ru: { translation: ru },
    en: { translation: en },
    'zh-CN': { translation: zhCN },
  },
  lng: initialLanguage,
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
  returnNull: false,
});

export default i18n;
