import type { Settings } from './types';

/** The fields a preset fills in. Everything else the user keeps. */
export type LLMPresetValues = Partial<Settings['llm']>;

export type LLMPresetId = 'local' | 'openai' | 'anthropic' | 'custom';

export interface LLMPreset {
  id: LLMPresetId;
  /** Empty for the custom preset: it fills nothing in. */
  values: LLMPresetValues;
  /** Hosts that identify a saved configuration as this preset. */
  hosts: string[];
  /** Whether the endpoint needs a key. Drives the hint shown under the field. */
  needsKey: boolean;
}

/**
 * Endpoints verified against vendor documentation on 2026-08-16:
 *
 *   - OpenAI serves the chat-completions API at https://api.openai.com/v1.
 *   - Anthropic exposes an OpenAI-compatible layer at https://api.anthropic.com/v1/
 *     that accepts the same `Authorization: Bearer` header this client sends. Its
 *     own documentation calls it a compatibility layer for evaluation rather than
 *     a production surface, which is why the UI says so too.
 *
 * Model ids age faster than endpoints, so a preset sets a current default and
 * leaves the field editable rather than constraining it to a list.
 */
export const LLM_PRESETS: LLMPreset[] = [
  {
    id: 'local',
    values: {
      base_url: 'http://llm:8080/v1',
      model: 'Qwen3-8B',
      context_size: 16384,
      max_tokens: 1800,
      max_concurrent_requests: 1,
      timeout_seconds: 180,
    },
    hosts: ['llm', 'localhost', '127.0.0.1', 'host.docker.internal'],
    needsKey: false,
  },
  {
    id: 'openai',
    values: {
      base_url: 'https://api.openai.com/v1',
      model: 'gpt-5.6-terra',
      context_size: 128000,
      max_tokens: 4000,
      max_concurrent_requests: 4,
      timeout_seconds: 120,
    },
    hosts: ['api.openai.com'],
    needsKey: true,
  },
  {
    id: 'anthropic',
    values: {
      base_url: 'https://api.anthropic.com/v1',
      model: 'claude-sonnet-5',
      context_size: 200000,
      max_tokens: 4000,
      max_concurrent_requests: 4,
      timeout_seconds: 120,
    },
    hosts: ['api.anthropic.com'],
    needsKey: true,
  },
  { id: 'custom', values: {}, hosts: [], needsKey: false },
];

export function presetById(id: LLMPresetId): LLMPreset {
  return LLM_PRESETS.find((preset) => preset.id === id) ?? LLM_PRESETS[LLM_PRESETS.length - 1];
}

/**
 * Identifies a saved configuration by the host it points at, so reopening the
 * settings screen selects what is actually configured instead of a default.
 * Only the host is matched: the path, the port of a local server and every
 * other field are the user's to change without losing the label.
 */
export function detectPreset(baseURL: string): LLMPresetId {
  const host = hostOf(baseURL);
  if (!host) return 'custom';
  for (const preset of LLM_PRESETS) {
    if (preset.hosts.includes(host)) return preset.id;
  }
  return 'custom';
}

function hostOf(baseURL: string): string {
  const trimmed = baseURL.trim();
  if (!trimmed) return '';
  try {
    return new URL(trimmed).hostname.toLowerCase();
  } catch {
    // A value the user is still typing is not an error worth reporting.
    return '';
  }
}
