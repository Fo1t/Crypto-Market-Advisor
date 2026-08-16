import { describe, expect, it } from 'vitest';

import { LLM_PRESETS, detectPreset, presetById } from '../api/llmPresets';

describe('LLM presets', () => {
  it('recognises the endpoint a saved configuration points at', () => {
    expect(detectPreset('http://llm:8080/v1')).toBe('local');
    expect(detectPreset('http://host.docker.internal:8080/v1')).toBe('local');
    expect(detectPreset('http://127.0.0.1:1234/v1')).toBe('local');
    expect(detectPreset('https://api.openai.com/v1')).toBe('openai');
    expect(detectPreset('https://api.anthropic.com/v1')).toBe('anthropic');
  });

  it('matches on the host alone, so port and path stay the user\'s to change', () => {
    expect(detectPreset('http://localhost:9999/openai/v1')).toBe('local');
    expect(detectPreset('https://api.openai.com/v1/')).toBe('openai');
  });

  it('calls anything else custom, including a value being typed', () => {
    expect(detectPreset('https://my-gateway.internal/v1')).toBe('custom');
    expect(detectPreset('')).toBe('custom');
    expect(detectPreset('https:/')).toBe('custom');
  });

  it('leaves every field alone for the custom preset', () => {
    expect(presetById('custom').values).toEqual({});
  });

  it('gives every non-custom preset an endpoint its own detection agrees with', () => {
    for (const preset of LLM_PRESETS) {
      if (preset.id === 'custom') continue;
      const baseURL = preset.values.base_url;
      expect(baseURL, `${preset.id} must set a base URL`).toBeTruthy();
      expect(detectPreset(baseURL!), `${preset.id} round-trip`).toBe(preset.id);
      expect(preset.values.model, `${preset.id} must set a model`).toBeTruthy();
    }
  });
});
