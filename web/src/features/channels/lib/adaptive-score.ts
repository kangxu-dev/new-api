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
import type { AdaptiveChannelScoresResponse } from '../types'

export type ChannelScoreEntry = {
  group: string
  model: string
  scored: boolean
  multiplier: number
  avgFirstByteMs: number
  requests: number
  serverErrors: number
}

export type ChannelScoreSummary = {
  /** True when at least one of the channel's entries reached the sample threshold. */
  scored: boolean
  /** Mean multiplier over the scored entries; 0 when nothing is scored. */
  multiplier: number
  entries: ChannelScoreEntry[]
}

/**
 * Rolls the per group-and-model score report up to one summary per channel.
 *
 * The API reports scores per (group, model) because that is the key the
 * selector uses, but the channel list shows channels. Averaging the scored
 * entries gives one number per channel; the per-entry detail stays available
 * for the tooltip.
 */
export function summarizeChannelScores(
  response: AdaptiveChannelScoresResponse | undefined
): Map<number, ChannelScoreSummary> {
  const summaries = new Map<number, ChannelScoreSummary>()
  for (const group of response?.groups ?? []) {
    for (const channel of group.channels) {
      let summary = summaries.get(channel.channel_id)
      if (!summary) {
        summary = { scored: false, multiplier: 0, entries: [] }
        summaries.set(channel.channel_id, summary)
      }
      summary.entries.push({
        group: group.group,
        model: group.model,
        scored: channel.scored,
        multiplier: channel.multiplier,
        avgFirstByteMs: channel.avg_first_byte_ms,
        requests: channel.requests,
        serverErrors: channel.server_errors,
      })
    }
  }
  for (const summary of summaries.values()) {
    const scored = summary.entries.filter((entry) => entry.scored)
    if (scored.length === 0) {
      continue
    }
    summary.scored = true
    summary.multiplier =
      scored.reduce((total, entry) => total + entry.multiplier, 0) /
      scored.length
  }
  return summaries
}

export function formatScoreMultiplier(multiplier: number): string {
  return `×${multiplier.toFixed(2)}`
}
