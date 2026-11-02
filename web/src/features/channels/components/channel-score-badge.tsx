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
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import {
  formatScoreMultiplier,
  type ChannelScoreEntry,
  type ChannelScoreSummary,
} from '../lib/adaptive-score'

function unscoredLiveMessage(
  entry: ChannelScoreEntry,
  effectiveSource: ChannelScoreSummary['source'],
  target: number,
  t: TFunction
): string {
  if (target > 0 && entry.requests >= target) {
    return t('Neutral: every sampled channel failed or produced no preference')
  }
  if (entry.requests > 0) {
    if (effectiveSource === 'probe') {
      return t('Below the sample threshold; the baseline below is used instead')
    }
    return t('Below the sample threshold; keeps its configured weight')
  }
  return t('No samples in the scoring window; routing stays neutral.')
}

/**
 * Renders the adaptive-routing score for one channel card. Measured business
 * samples, a probe baseline, and a neutral waiting state stay visually
 * distinct; a baseline is never presented as measured traffic.
 */
export function ChannelScoreBadge(props: {
  summary: ChannelScoreSummary | undefined
  probeEnabled: boolean
  minSamples: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const summary = props.summary
  const target = Math.max(0, props.minSamples)
  const targetLabel = formatNumber(target, locale)
  const probeEntries = summary?.entries.filter(
    (entry) => entry.source === 'probe'
  )
  const liveEntries = summary?.entries.filter(
    (entry) => entry.source === 'live'
  )
  const probeSamples = probeEntries?.reduce(
    (most, entry) => Math.max(most, entry.requests),
    0
  )
  const liveSamples = liveEntries?.reduce(
    (most, entry) => Math.max(most, entry.requests),
    0
  )
  const liveReached =
    target > 0 &&
    (liveEntries?.some((entry) => entry.requests >= target) ?? false)
  const sampling = props.probeEnabled && (probeEntries?.length ?? 0) > 0
  const effectiveSource = summary?.source ?? ''

  if (!summary?.scored) {
    let status = t('Awaiting samples')
    if (liveReached) {
      status = t('Neutral')
    } else if (sampling) {
      status = t('Sampling')
    }
    return (
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger
            render={
              <span
                tabIndex={0}
                aria-label={`${t('Adaptive score')}: ${formatScoreMultiplier(1)} ${status}`}
                className='focus-visible:ring-ring flex cursor-help items-center gap-1 rounded-sm text-sm focus-visible:ring-2 focus-visible:outline-none'
              >
                <span className='tabular-nums'>{formatScoreMultiplier(1)}</span>
                <StatusBadge
                  type='text'
                  copyable={false}
                  variant='neutral'
                  label={status}
                  className='text-[11px]'
                />
              </span>
            }
          />
          <TooltipContent>
            <div className='space-y-1'>
              <p className='font-medium'>{t('Adaptive score')}</p>
              {liveReached ? (
                liveEntries?.map((entry) => (
                  <p key={`${entry.group}/${entry.model}`}>
                    {`${entry.group} · ${entry.model}: `}
                    {entry.requests > 0
                      ? `${formatNumber(entry.requests, locale)}/${targetLabel} · `
                      : ''}
                    {unscoredLiveMessage(entry, effectiveSource, target, t)}
                  </p>
                ))
              ) : (
                <>
                  {sampling ? (
                    <p>
                      {t(
                        'Probe sampling has {{samples}}/{{target}} samples; routing stays neutral until the threshold is reached.',
                        {
                          samples: formatNumber(probeSamples ?? 0, locale),
                          target: targetLabel,
                        }
                      )}
                    </p>
                  ) : null}
                  {liveEntries?.map((entry) => (
                    <p key={`${entry.group}/${entry.model}`}>
                      {`${entry.group} · ${entry.model}: `}
                      {entry.requests > 0
                        ? `${formatNumber(entry.requests, locale)}/${targetLabel} · `
                        : ''}
                      {unscoredLiveMessage(entry, effectiveSource, target, t)}
                    </p>
                  ))}
                  {!sampling && (liveSamples ?? 0) === 0 ? (
                    <p>
                      {t(
                        'No samples in the scoring window; routing stays neutral.'
                      )}
                    </p>
                  ) : null}
                </>
              )}
            </div>
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>
    )
  }

  const mixed =
    (probeEntries?.length ?? 0) > 0 ||
    (liveEntries?.some((entry) => !entry.scored) ?? false)
  let status = t('Measured')
  let statusVariant: 'warning' | 'success' = 'success'
  if (summary.source === 'probe') {
    status = t('Baseline')
    statusVariant = 'warning'
  } else if (mixed) {
    status = t('Mixed')
    statusVariant = 'warning'
  }
  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger
          render={
            <span
              tabIndex={0}
              aria-label={`${t('Adaptive score')}: ${formatScoreMultiplier(summary.multiplier)} ${status}`}
              className='focus-visible:ring-ring flex cursor-help items-center gap-1 rounded-sm text-sm focus-visible:ring-2 focus-visible:outline-none'
            >
              <span className='tabular-nums'>
                {formatScoreMultiplier(summary.multiplier)}
              </span>
              <StatusBadge
                type='text'
                copyable={false}
                variant={statusVariant}
                label={status}
                className='text-[11px]'
              />
            </span>
          }
        />
        <TooltipContent>
          <div className='space-y-1'>
            <p className='font-medium'>{t('Adaptive score')}</p>
            {summary.entries.map((entry) =>
              entry.source === 'probe' ? (
                <p key={`probe/${entry.model}`}>
                  {entry.scored
                    ? t(
                        'Baseline {{model}}: {{multiplier}} · {{latency}}ms · {{samples}}/{{target}} samples · {{errors}} errors',
                        {
                          model: entry.model,
                          multiplier: formatScoreMultiplier(entry.multiplier),
                          latency: formatNumber(
                            Math.round(entry.avgFirstByteMs),
                            locale
                          ),
                          samples: formatNumber(entry.requests, locale),
                          target: targetLabel,
                          errors: formatNumber(entry.serverErrors, locale),
                        }
                      )
                    : t(
                        'Sampling baseline {{model}}: {{samples}}/{{target}} samples',
                        {
                          model: entry.model,
                          samples: formatNumber(entry.requests, locale),
                          target: targetLabel,
                        }
                      )}
                </p>
              ) : (
                <p key={`${entry.group}/${entry.model}`}>
                  {`${entry.group} · ${entry.model}: `}
                  {entry.scored
                    ? t(
                        '{{multiplier}} · {{latency}}ms · {{samples}} samples · {{errors}} errors',
                        {
                          multiplier: formatScoreMultiplier(entry.multiplier),
                          latency: formatNumber(
                            Math.round(entry.avgFirstByteMs),
                            locale
                          ),
                          samples: formatNumber(entry.requests, locale),
                          errors: formatNumber(entry.serverErrors, locale),
                        }
                      )
                    : unscoredLiveMessage(entry, summary.source, target, t)}
                </p>
              )
            )}
          </div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
