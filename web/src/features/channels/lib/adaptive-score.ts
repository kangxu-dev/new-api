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

export type ChannelScoreSource = 'live' | 'probe'

export type ChannelScoreEntry = {
  group: string
  model: string
  source: ChannelScoreSource
  /** For live entries, whether the effective multiplier came from the baseline. */
  effectiveSource: '' | 'live' | 'probe'
  /** Probe model, set on baseline entries and on live entries with a probe fallback. */
  probeModel: string
  scored: boolean
  multiplier: number
  avgFirstByteMs: number
  requests: number
  serverErrors: number
}

export type ChannelScoreSummary = {
  /** True when at least one entry carries a multiplier. */
  scored: boolean
  /** Mean multiplier over the winning source (live over probe); 0 when unscored. */
  multiplier: number
  /** Which source produced the winning multiplier; empty when unscored. */
  source: ChannelScoreSource | ''
  entries: ChannelScoreEntry[]
  minSamples: number
  probeEnabled: boolean
  probeModel: string
}

/**
 * Rolls the per group-and-model score report up to one summary per channel.
 *
 * Business entries and probe baselines are kept apart: a multiplier is averaged
 * only within its own source, and a measured business score always wins over a
 * probe baseline. The probe baseline is what lets a channel show a score before
 * any business request has reached MinSamples.
 */
export function summarizeChannelScores(
  response: AdaptiveChannelScoresResponse | undefined
): Map<number, ChannelScoreSummary> {
  const summaries = new Map<number, ChannelScoreSummary>()
  const getSummary = (channelID: number) => {
    let summary = summaries.get(channelID)
    if (!summary) {
      summary = {
        scored: false,
        multiplier: 0,
        source: '',
        entries: [],
        minSamples: response?.min_samples ?? 0,
        probeEnabled: response?.probe_enabled ?? false,
        probeModel: response?.probe_model ?? '',
      }
      summaries.set(channelID, summary)
    }
    return summary
  }

  for (const group of response?.groups ?? []) {
    for (const channel of group.channels) {
      getSummary(channel.channel_id).entries.push({
        group: group.group,
        model: group.model,
        source: 'live',
        effectiveSource: channel.effective_source,
        probeModel:
          channel.effective_source === 'probe'
            ? (channel.probe_model ?? '')
            : '',
        // A live entry is scored only by business samples. A probe fallback is
        // represented by the separate probe_baselines entry below, never by
        // re-labelling business counters as probe data.
        scored: channel.effective_source === 'live' && channel.scored,
        multiplier:
          channel.effective_source === 'live' ? channel.multiplier : 0,
        avgFirstByteMs: channel.avg_first_byte_ms,
        requests: channel.requests,
        serverErrors: channel.server_errors,
      })
    }
  }
  for (const baseline of response?.probe_baselines ?? []) {
    getSummary(baseline.channel_id).entries.push({
      group: '',
      model: baseline.probe_model,
      source: 'probe',
      effectiveSource: 'probe',
      probeModel: baseline.probe_model,
      scored: baseline.scored,
      multiplier: baseline.multiplier,
      avgFirstByteMs: baseline.avg_first_byte_ms,
      requests: baseline.requests,
      serverErrors: baseline.server_errors,
    })
  }

  for (const summary of summaries.values()) {
    const target = summary.minSamples
    const live = summary.entries.filter(
      (entry) => entry.source === 'live' && entry.scored
    )
    const liveReached = summary.entries.some(
      (entry) =>
        entry.source === 'live' && target > 0 && entry.requests >= target
    )
    const probe = summary.entries.filter(
      (entry) => entry.source === 'probe' && entry.scored
    )
    // Measured business data is authoritative once it reaches the threshold,
    // even when ratio mode returns neutral because every sampled channel
    // failed. A baseline must not hide that live result.
    if (live.length > 0) {
      summary.scored = true
      summary.source = 'live'
      summary.multiplier =
        live.reduce((total, entry) => total + entry.multiplier, 0) / live.length
      continue
    }
    if (liveReached) {
      continue
    }
    if (probe.length > 0) {
      summary.scored = true
      summary.source = 'probe'
      summary.multiplier =
        probe.reduce((total, entry) => total + entry.multiplier, 0) /
        probe.length
    }
  }
  return summaries
}

export function formatScoreMultiplier(multiplier: number): string {
  return `×${multiplier.toFixed(2)}`
}
