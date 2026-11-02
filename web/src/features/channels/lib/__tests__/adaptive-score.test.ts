/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, test } from 'vitest'

import type { AdaptiveChannelScoresResponse } from '../../types'
import {
  formatScoreMultiplier,
  summarizeChannelScores,
} from '../adaptive-score'

function score(
  channelId: number,
  scored: boolean,
  multiplier: number,
  effectiveSource: '' | 'live' | 'probe' = scored ? 'live' : '',
  requests = 20
) {
  return {
    channel_id: channelId,
    channel_name: `channel-${channelId}`,
    requests,
    successes: scored ? requests : 12,
    server_errors: scored ? 0 : 8,
    avg_first_byte_ms: 7000,
    scored,
    multiplier,
    effective_source: effectiveSource,
  }
}

function baseline(
  channelId: number,
  scored: boolean,
  multiplier: number,
  requests = 20
) {
  return {
    channel_id: channelId,
    channel_name: `channel-${channelId}`,
    probe_model: 'probe-model',
    requests,
    successes: scored ? requests : 0,
    server_errors: 0,
    avg_first_byte_ms: 800,
    scored,
    multiplier,
    source: 'probe' as const,
  }
}

function response(
  groups: AdaptiveChannelScoresResponse['groups'],
  probeBaselines: AdaptiveChannelScoresResponse['probe_baselines'] = []
): AdaptiveChannelScoresResponse {
  return {
    enabled: true,
    latency_mode: 'ratio',
    window_minutes: 30,
    min_samples: 10,
    probe_enabled: probeBaselines.length > 0,
    probe_model: probeBaselines.length > 0 ? 'probe-model' : '',
    groups,
    probe_baselines: probeBaselines,
  }
}

describe('summarizeChannelScores', () => {
  test('averages a channel across every group and model it appears in', () => {
    const summaries = summarizeChannelScores(
      response([
        {
          group: 'codex',
          model: 'gpt-a',
          channels: [score(1, true, 1.0), score(2, true, 1.5)],
        },
        {
          group: 'codex',
          model: 'gpt-b',
          channels: [score(1, true, 1.4)],
        },
      ])
    )

    expect(summaries.get(1)?.scored).toBe(true)
    expect(summaries.get(1)?.multiplier).toBeCloseTo(1.2)
    expect(summaries.get(1)?.entries).toHaveLength(2)
    expect(summaries.get(2)?.multiplier).toBeCloseTo(1.5)
  })

  test('reports an unscored channel as unscored rather than neutral', () => {
    // A channel below the sample threshold keeps its configured weight, which
    // looks exactly like a running score that finds the channels equal. The
    // summary has to keep the two apart.
    const summaries = summarizeChannelScores(
      response([
        {
          group: 'codex',
          model: 'gpt-a',
          channels: [score(1, true, 1.5), score(2, false, 0)],
        },
      ])
    )

    expect(summaries.get(1)?.scored).toBe(true)
    expect(summaries.get(2)?.scored).toBe(false)
    expect(summaries.get(2)?.multiplier).toBe(0)
    expect(summaries.get(2)?.entries).toHaveLength(1)
  })

  test('ignores unscored entries when averaging', () => {
    const summaries = summarizeChannelScores(
      response([
        {
          group: 'codex',
          model: 'gpt-a',
          channels: [score(1, true, 1.5), score(1, false, 0)],
        },
      ])
    )

    expect(summaries.get(1)?.multiplier).toBeCloseTo(1.5)
  })

  test('returns nothing when the report is missing or empty', () => {
    expect(summarizeChannelScores(undefined).size).toBe(0)
    expect(summarizeChannelScores(response([])).size).toBe(0)
  })

  test('shows a probe baseline when no business group has samples', () => {
    const summaries = summarizeChannelScores(
      response([], [baseline(1, true, 1.2), baseline(2, false, 0, 3)])
    )

    expect(summaries.get(1)?.scored).toBe(true)
    expect(summaries.get(1)?.source).toBe('probe')
    expect(summaries.get(1)?.multiplier).toBeCloseTo(1.2)
    expect(summaries.get(1)?.probeModel).toBe('probe-model')
    expect(summaries.get(1)?.entries[0].source).toBe('probe')
    expect(summaries.get(2)?.scored).toBe(false)
    expect(summaries.get(2)?.entries[0].requests).toBe(3)
  })

  test('never averages a baseline into measured business samples', () => {
    const summaries = summarizeChannelScores(
      response(
        [
          {
            group: 'codex',
            model: 'gpt-a',
            channels: [score(1, true, 1.4), score(1, false, 0)],
          },
        ],
        [baseline(1, true, 0.8)]
      )
    )

    const summary = summaries.get(1)
    expect(summary?.source).toBe('live')
    expect(summary?.multiplier).toBeCloseTo(1.4)
    expect(summary?.entries).toHaveLength(3)
    expect(
      summary?.entries.filter((entry) => entry.source === 'live')
    ).toHaveLength(2)
    expect(
      summary?.entries.filter((entry) => entry.source === 'probe')
    ).toHaveLength(1)
  })

  test('uses the baseline when measured business samples are still below the threshold', () => {
    const summaries = summarizeChannelScores(
      response(
        [
          {
            group: 'codex',
            model: 'gpt-a',
            channels: [score(1, false, 0, '', 3)],
          },
        ],
        [baseline(1, true, 1.1)]
      )
    )

    expect(summaries.get(1)?.source).toBe('probe')
    expect(summaries.get(1)?.multiplier).toBeCloseTo(1.1)
  })

  test('keeps a reached but neutral live channel unscored instead of borrowing a baseline', () => {
    const summaries = summarizeChannelScores(
      response(
        [
          {
            group: 'codex',
            model: 'gpt-a',
            channels: [score(1, false, 0, '', 20)],
          },
        ],
        [baseline(1, true, 1.4)]
      )
    )

    expect(summaries.get(1)?.scored).toBe(false)
    expect(summaries.get(1)?.source).toBe('')
    expect(summaries.get(1)?.multiplier).toBe(0)
  })

  test('does not re-label a live entry with a probe fallback as a probe sample', () => {
    const summaries = summarizeChannelScores(
      response(
        [
          {
            group: 'codex',
            model: 'gpt-a',
            channels: [score(1, true, 1.3, 'probe', 3)],
          },
        ],
        [baseline(1, true, 1.3)]
      )
    )

    expect(summaries.get(1)?.entries).toHaveLength(2)
    expect(summaries.get(1)?.entries[0].source).toBe('live')
    expect(summaries.get(1)?.entries[0].requests).toBe(3)
    expect(summaries.get(1)?.entries[1].source).toBe('probe')
    expect(summaries.get(1)?.multiplier).toBeCloseTo(1.3)
  })
})

describe('formatScoreMultiplier', () => {
  test('always shows two decimals so the column does not jump', () => {
    expect(formatScoreMultiplier(1.5)).toBe('×1.50')
    expect(formatScoreMultiplier(0.666)).toBe('×0.67')
    expect(formatScoreMultiplier(1)).toBe('×1.00')
  })
})
