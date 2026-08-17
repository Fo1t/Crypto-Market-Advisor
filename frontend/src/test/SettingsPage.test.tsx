import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { api } from '../api/client';
import i18n, { setLanguage } from '../i18n';
import { SettingsPage } from '../pages/SettingsPage';

const settings = {
  general: {
    language: 'en',
    analysis_interval_seconds: 300,
    timeframes: ['1h', '4h', '1d'],
    analysis_enabled: true,
  },
  llm: {
    base_url: 'http://llm:8080/v1',
    model: 'Qwen3-8B',
    timeout_seconds: 180,
    temperature: 0.2,
    max_tokens: 1800,
    context_size: 16384,
    enabled: true,
    max_concurrent_requests: 1,
    prompt_version: 'v1',
  },
  risk: {
    min_leverage: 1,
    max_leverage: 20,
    max_recommended_allocation_pct: '5',
    risk_per_trade_pct: 0.75,
    high_volatility_atr_pct: 3,
    extreme_volatility_atr_pct: 6,
    min_confidence: 60,
    critical_news_max_leverage: 3,
    critical_news_high_vol_max_leverage: 2,
    critical_news_max_age_seconds: 3600,
  },
  news: {
    enabled: true,
    fetch_interval_seconds: 900,
    llm_lookback_hours: 24,
    llm_max_asset_items: 10,
    llm_max_global_items: 10,
    history_min_sample_size: 5,
    bybit_enabled: true,
  },
  exchange: {
    exchange: 'bybit',
    maker_fee_pct: '0.02',
    taker_fee_pct: '0.055',
    slippage_pct: '0.05',
    fees_configured: true,
  },
  strategies: { min_signal: 1, items: [] },
  updated_at: '2026-08-17T00:00:00Z',
};

describe('SettingsPage', () => {
  beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.spyOn(api, 'settings').mockResolvedValue(settings as never);
    vi.spyOn(api, 'hiddenBacktests').mockResolvedValue({ runs: 0, trades: 0, size: '0 B' } as never);
    vi.spyOn(api, 'markets').mockResolvedValue({ items: [], total: 0, limit: 0, offset: 0 } as never);
    vi.spyOn(api, 'importStatus').mockResolvedValue({
      status: 'idle',
      symbols: [],
      timeframes: [],
      items: [],
    } as never);
    vi.spyOn(api, 'strategyCatalog').mockResolvedValue({ items: [], default_min_signal: 1 } as never);
  });

  afterEach(async () => {
    cleanup();
    vi.restoreAllMocks();
    await i18n.changeLanguage('en');
  });

  it('follows a language switched somewhere else, so both switchers agree', async () => {
    render(<SettingsPage />);
    await screen.findByRole('button', { name: 'English' });

    expect(screen.getByRole('button', { name: 'English' }).getAttribute('aria-pressed')).toBe('true');

    // What the sidebar switcher does: change the language directly, without
    // touching this form.
    await act(async () => setLanguage('ru'));

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Русский' }).getAttribute('aria-pressed')).toBe('true'),
    );
    expect(screen.getByRole('button', { name: 'English' }).getAttribute('aria-pressed')).toBe('false');
  });

  it('selects the custom LLM provider even though it fills nothing in', async () => {
    render(<SettingsPage />);

    const custom = await screen.findByRole('button', { name: 'Custom' });
    expect(custom.className).not.toContain('tab--active');
    // The bundled endpoint is detected as "Local" until the user says otherwise.
    expect(screen.getByRole('button', { name: 'Local' }).className).toContain('tab--active');

    fireEvent.click(custom);

    await waitFor(() => expect(custom.className).toContain('tab--active'));
    expect(screen.getByRole('button', { name: 'Local' }).className).not.toContain('tab--active');
    // Choosing "custom" must not rewrite the endpoint the user already has.
    expect((screen.getByLabelText('Base URL') as HTMLInputElement).value).toBe('http://llm:8080/v1');
  });
});
