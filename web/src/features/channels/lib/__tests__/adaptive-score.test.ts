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
import { formatScoreMultiplier, summarizeChannelScores } from '../adaptive-score'

function score(channelId: number, scored: boolean, multiplier: number) {
  return {
    channel_id: channelId,
    channel_name: `channel-${channelId}`,
    requests: 20,
    successes: scored ? 20 : 12,
    server_errors: scored ? 0 : 8,
    avg_first_byte_ms: 7000,
    scored,
    multiplier,
  }
}

function response(
  groups: AdaptiveChannelScoresResponse['groups']
): AdaptiveChannelScoresResponse {
  return {
    enabled: true,
    latency_mode: 'ratio',
    window_minutes: 30,
    min_samples: 10,
    groups,
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
})

describe('formatScoreMultiplier', () => {
  test('always shows two decimals so the column does not jump', () => {
    expect(formatScoreMultiplier(1.5)).toBe('×1.50')
    expect(formatScoreMultiplier(0.666)).toBe('×0.67')
    expect(formatScoreMultiplier(1)).toBe('×1.00')
  })
})
