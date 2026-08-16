import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { api } from '../api/client';
import i18n from '../i18n';
import { BacktestingPage } from '../pages/BacktestingPage';

const previousRun = {
  id: 'run-1',
  mode: 'technical',
  symbol: 'ETH',
  timeframe: '4h',
  date_from: '2026-05-02T00:00:00Z',
  date_to: '2026-06-03T23:59:59Z',
  analysis_interval: '8h',
  status: 'completed',
  estimated_steps: 96,
  completed_steps: 96,
  created_at: '2026-06-04T00:00:00Z',
  params: {
    mode: 'technical',
    symbol: 'ETH',
    timeframe: '4h',
    date_from: '2026-05-02T00:00:00Z',
    date_to: '2026-06-03T23:59:59Z',
    analysis_interval: '8h',
    initial_capital: '25000',
    allocation_pct: '7',
    leverage: '17',
    slippage_pct: '0.05',
    funding_rate_pct: '0.01',
    maintenance_margin_pct: '0.5',
    max_open_positions: 3,
    min_confidence: 61,
    use_cache: false,
    exit_mode: 'pnl_ladder',
    take_profit_ladder: [
      { pnl_pct: 40, close_pct: 60 },
      { pnl_pct: 90, close_pct: 40 },
    ],
    stop_loss_ladder: [{ pnl_pct: 30, close_pct: 100 }],
  },
};

describe('BacktestingPage copy', () => {
  beforeEach(async () => {
    await i18n.changeLanguage('ru');
    vi.spyOn(api, 'backtests').mockResolvedValue({
      items: [previousRun],
      total: 1,
      limit: 25,
      offset: 0,
    } as never);
    vi.spyOn(api, 'markets').mockResolvedValue({
      items: [
        { id: 2, symbol: 'ETH', display_name: 'Ethereum' },
        { id: 1, symbol: 'BTC', display_name: 'Bitcoin' },
      ],
      total: 2,
      limit: 100,
      offset: 0,
    } as never);
    window.scrollTo = vi.fn();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it('refills every parameter of a previous run, including dates, interval and leverage', async () => {
    render(<BacktestingPage />);

    const copy = await screen.findByRole('button', { name: 'Скопировать' });
    fireEvent.click(copy);

    const field = (label: string | RegExp) => screen.getByLabelText(label) as HTMLInputElement;
    await waitFor(() => expect(field(/^Начало$/).value).toBe('2026-05-02'));

    expect(field(/^Конец$/).value).toBe('2026-06-03');
    expect(field(/^Интервал анализа$/).value).toBe('8h');
    expect(field(/^Плечо$/).value).toBe('17');
    expect(field(/^Начальный капитал$/).value).toBe('25000');
    expect(field(/^Доля капитала/).value).toBe('7');
    expect(field(/^Проскальзывание/).value).toBe('0.05');
    expect(field(/^Максимум одновременных позиций$/).value).toBe('3');
    expect(field(/^Мин. уверенность$/).value).toBe('61');
    expect(field(/^Фиксация прибыли/).value).toBe('40:60, 90:40');
    expect(field(/^Фиксация убытка/).value).toBe('30:100');
  });
});
