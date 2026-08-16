import { describe, expect, it } from 'vitest';

import { ru } from '../i18n/locales/ru';
import { en } from '../i18n/locales/en';
import { zhCN } from '../i18n/locales/zh-CN';
import { setLanguage, SUPPORTED_LANGUAGES } from '../i18n';

type Dict = Record<string, unknown>;

/** Flattens a nested dictionary into dotted keys. */
function flatten(input: Dict, prefix = ''): string[] {
  return Object.entries(input).flatMap(([key, value]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      return flatten(value as Dict, path);
    }
    return [path];
  });
}

describe('i18n', () => {
  // English is the default language and the shape the others are checked
  // against, so a key added there has to reach every locale before it ships.
  const englishKeys = flatten(en as unknown as Dict).sort();

  it('supports exactly the three required languages, English first', () => {
    expect([...SUPPORTED_LANGUAGES]).toEqual(['en', 'ru', 'zh-CN']);
  });

  it('Russian covers every English key', () => {
    expect(flatten(ru as unknown as Dict).sort()).toEqual(englishKeys);
  });

  it('Simplified Chinese covers every English key', () => {
    expect(flatten(zhCN as unknown as Dict).sort()).toEqual(englishKeys);
  });

  it('has no empty translations', () => {
    for (const [name, dict] of [
      ['ru', ru],
      ['en', en],
      ['zh-CN', zhCN],
    ] as const) {
      const empty = flatten(dict as unknown as Dict).filter((key) => {
        const value = key.split('.').reduce<unknown>((acc, part) => (acc as Dict)?.[part], dict);
        return typeof value !== 'string' || value.trim() === '';
      });
      expect(empty, `${name} has empty values`).toEqual([]);
    }
  });

  it('translates every backend action enum', () => {
    for (const action of ['OPEN_LONG', 'OPEN_SHORT', 'NO_ACTION', 'MANAGE_POSITION'] as const) {
      expect(ru.enums.action[action]).toBeTruthy();
      expect(en.enums.action[action]).toBeTruthy();
      expect(zhCN.enums.action[action]).toBeTruthy();
    }
  });

  it('keeps the document language in sync for accessibility', () => {
    setLanguage('ru');
    expect(document.documentElement.lang).toBe('ru');
    setLanguage('zh-CN');
    expect(document.documentElement.lang).toBe('zh-CN');
    setLanguage('en');
  });
});
