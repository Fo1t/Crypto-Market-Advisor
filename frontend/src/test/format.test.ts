import { describe, expect, it } from 'vitest';

import { formatCompact, formatMinutes, formatPct, formatPrice, toNumber, toneOf } from '../utils/format';

describe('format helpers', () => {
  it('formats prices with magnitude-aware precision', () => {
    expect(formatPrice(117234.5)).toBe('117,234.50');
    expect(formatPrice(1.2345678)).toBe('1.2346');
    expect(formatPrice(0.00012345)).toBe('0.00012345');
  });

  it('renders missing values as a dash instead of NaN', () => {
    expect(formatPrice(undefined)).toBe('—');
    expect(formatPct(null)).toBe('—');
    expect(formatCompact('')).toBe('—');
    expect(formatMinutes(undefined)).toBe('—');
  });

  it('signs percentages explicitly', () => {
    expect(formatPct(4.567)).toBe('+4.57%');
    expect(formatPct(-4.567)).toBe('-4.57%');
    expect(formatPct(0)).toBe('+0.00%');
  });

  it('compacts large numbers', () => {
    expect(formatCompact(1_500)).toBe('1.50K');
    expect(formatCompact(2_300_000)).toBe('2.30M');
    expect(formatCompact(4_100_000_000)).toBe('4.10B');
  });

  it('parses decimal strings coming from the backend', () => {
    expect(toNumber('123.45')).toBe(123.45);
    expect(toNumber('not a number')).toBeNull();
    expect(toNumber(null)).toBeNull();
  });

  it('derives tone from sign', () => {
    expect(toneOf('12.5')).toBe('long');
    expect(toneOf(-3)).toBe('short');
    expect(toneOf(0)).toBeUndefined();
  });

  it('formats holding time in the largest sensible unit', () => {
    expect(formatMinutes(45)).toBe('45m');
    expect(formatMinutes(120)).toBe('2.0h');
    expect(formatMinutes(4320)).toBe('3.0d');
  });
});
