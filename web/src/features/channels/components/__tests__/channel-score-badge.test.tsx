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
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeAll, describe, expect, it } from 'vitest'

import type { ChannelScoreSummary } from '../../lib/adaptive-score'
import { ChannelScoreBadge } from '../channel-score-badge'

const translations = {
  Baseline: 'Baseline',
  Measured: 'Measured',
  Mixed: 'Mixed',
  Neutral: 'Neutral',
  Sampling: 'Sampling',
  'Awaiting samples': 'Awaiting samples',
  'Adaptive score': 'Adaptive score',
  'Baseline {{model}}: {{multiplier}} · {{latency}}ms · {{samples}}/{{target}} samples · {{errors}} errors':
    'Baseline {{model}}: {{multiplier}} · {{latency}}ms · {{samples}}/{{target}} samples · {{errors}} errors',
  'Sampling baseline {{model}}: {{samples}}/{{target}} samples':
    'Sampling baseline {{model}}: {{samples}}/{{target}} samples',
  'Probe sampling has {{samples}}/{{target}} samples; routing stays neutral until the threshold is reached.':
    'Probe sampling has {{samples}}/{{target}} samples; routing stays neutral until the threshold is reached.',
  'No samples in the scoring window; routing stays neutral.':
    'No samples in the scoring window; routing stays neutral.',
  'Neutral: every sampled channel failed or produced no preference':
    'Neutral: every sampled channel failed or produced no preference',
  'Below the sample threshold; the baseline below is used instead':
    'Below the sample threshold; the baseline below is used instead',
  'Below the sample threshold; keeps its configured weight':
    'Below the sample threshold; keeps its configured weight',
}

function summary(overrides: Partial<ChannelScoreSummary>): ChannelScoreSummary {
  return {
    scored: false,
    multiplier: 0,
    source: '',
    entries: [],
    minSamples: 10,
    probeEnabled: true,
    probeModel: 'probe-model',
    ...overrides,
  }
}

beforeAll(() => {
  i18next.addResourceBundle('en', 'translation', translations, true, true)
})

afterEach(() => {
  cleanup()
})

describe('ChannelScoreBadge', () => {
  it('marks a probe baseline as Baseline rather than measured traffic', async () => {
    render(
      <ChannelScoreBadge
        summary={summary({
          scored: true,
          multiplier: 1.2,
          source: 'probe',
          entries: [
            {
              group: '',
              model: 'probe-model',
              source: 'probe',
              effectiveSource: 'probe',
              probeModel: 'probe-model',
              scored: true,
              multiplier: 1.2,
              avgFirstByteMs: 800,
              requests: 10,
              serverErrors: 1,
            },
          ],
        })}
        probeEnabled
        minSamples={10}
      />
    )

    expect(screen.getByText('×1.20')).toBeVisible()
    expect(screen.getByText('Baseline')).toBeVisible()
    expect(screen.queryByText('Measured')).not.toBeInTheDocument()
    expect(
      screen.getByLabelText('Adaptive score: ×1.20 Baseline')
    ).toHaveAttribute('tabindex', '0')

    await userEvent.hover(screen.getByText('×1.20'))
    expect(
      await screen.findByText(
        /Baseline probe-model: ×1.20 · 800ms · 10\/10 samples/
      )
    ).toBeVisible()
  })

  it('shows a measured business score as Measured', () => {
    render(
      <ChannelScoreBadge
        summary={summary({
          scored: true,
          multiplier: 1.3,
          source: 'live',
          entries: [
            {
              group: 'codex',
              model: 'gpt-a',
              source: 'live',
              effectiveSource: 'live',
              probeModel: '',
              scored: true,
              multiplier: 1.3,
              avgFirstByteMs: 1200,
              requests: 20,
              serverErrors: 0,
            },
          ],
        })}
        probeEnabled
        minSamples={10}
      />
    )

    expect(screen.getByText('×1.30')).toBeVisible()
    expect(screen.getByText('Measured')).toBeVisible()
    expect(screen.queryByText('Baseline')).not.toBeInTheDocument()
  })

  it('shows neutral waiting and probe progress while samples are below target', async () => {
    render(
      <ChannelScoreBadge
        summary={summary({
          entries: [
            {
              group: '',
              model: 'probe-model',
              source: 'probe',
              effectiveSource: 'probe',
              probeModel: 'probe-model',
              scored: false,
              multiplier: 0,
              avgFirstByteMs: 0,
              requests: 3,
              serverErrors: 0,
            },
          ],
        })}
        probeEnabled
        minSamples={10}
      />
    )

    expect(screen.getByText('×1.00')).toBeVisible()
    expect(screen.getByText('Sampling')).toBeVisible()

    await userEvent.hover(screen.getByText('×1.00'))
    expect(
      await screen.findByText(
        'Probe sampling has 3/10 samples; routing stays neutral until the threshold is reached.'
      )
    ).toBeVisible()
  })

  it('shows a neutral waiting state without promising sampling when probes are disabled', async () => {
    render(
      <ChannelScoreBadge
        summary={undefined}
        probeEnabled={false}
        minSamples={10}
      />
    )

    expect(screen.getByText('×1.00')).toBeVisible()
    expect(screen.getByText('Awaiting samples')).toBeVisible()

    await userEvent.hover(screen.getByText('×1.00'))
    expect(
      await screen.findByText(
        'No samples in the scoring window; routing stays neutral.'
      )
    ).toBeVisible()
  })

  it('does not claim active sampling when the configured probe has no baseline entry', () => {
    render(
      <ChannelScoreBadge
        summary={summary({ entries: [] })}
        probeEnabled
        minSamples={10}
      />
    )

    expect(screen.getByText('Awaiting samples')).toBeVisible()
    expect(screen.queryByText('Sampling')).not.toBeInTheDocument()
  })

  it('shows live sample progress instead of claiming there are no samples', async () => {
    render(
      <ChannelScoreBadge
        summary={summary({
          entries: [
            {
              group: 'codex',
              model: 'gpt-a',
              source: 'live',
              effectiveSource: '',
              probeModel: '',
              scored: false,
              multiplier: 0,
              avgFirstByteMs: 900,
              requests: 3,
              serverErrors: 0,
            },
          ],
        })}
        probeEnabled={false}
        minSamples={10}
      />
    )

    expect(screen.getByText('Awaiting samples')).toBeVisible()
    await userEvent.hover(screen.getByText('×1.00'))
    expect(
      await screen.findByText(
        /3\/10 · Below the sample threshold; keeps its configured weight/
      )
    ).toBeVisible()
    expect(
      screen.queryByText(
        'No samples in the scoring window; routing stays neutral.'
      )
    ).not.toBeInTheDocument()
  })

  it('shows a neutral reached state instead of letting a baseline hide an all-failed live model', async () => {
    render(
      <ChannelScoreBadge
        summary={summary({
          entries: [
            {
              group: 'codex',
              model: 'gpt-a',
              source: 'live',
              effectiveSource: '',
              probeModel: '',
              scored: false,
              multiplier: 0,
              avgFirstByteMs: 0,
              requests: 20,
              serverErrors: 20,
            },
            {
              group: '',
              model: 'probe-model',
              source: 'probe',
              effectiveSource: 'probe',
              probeModel: 'probe-model',
              scored: true,
              multiplier: 1.4,
              avgFirstByteMs: 700,
              requests: 20,
              serverErrors: 0,
            },
          ],
        })}
        probeEnabled
        minSamples={10}
      />
    )

    expect(screen.getByText('Neutral')).toBeVisible()
    expect(screen.queryByText('Baseline')).not.toBeInTheDocument()
    await userEvent.hover(screen.getByText('×1.00'))
    expect(
      await screen.findByText(
        /20\/10 · Neutral: every sampled channel failed or produced no preference/
      )
    ).toBeVisible()
  })

  it('labels a mix of measured and baseline models as Mixed', () => {
    render(
      <ChannelScoreBadge
        summary={summary({
          scored: true,
          multiplier: 1.2,
          source: 'live',
          entries: [
            {
              group: 'codex',
              model: 'gpt-a',
              source: 'live',
              effectiveSource: 'live',
              probeModel: '',
              scored: true,
              multiplier: 1.2,
              avgFirstByteMs: 800,
              requests: 20,
              serverErrors: 0,
            },
            {
              group: '',
              model: 'probe-model',
              source: 'probe',
              effectiveSource: 'probe',
              probeModel: 'probe-model',
              scored: true,
              multiplier: 1.1,
              avgFirstByteMs: 900,
              requests: 20,
              serverErrors: 0,
            },
          ],
        })}
        probeEnabled
        minSamples={10}
      />
    )

    expect(screen.getByText('Mixed')).toBeVisible()
  })

  it('opens the tooltip from keyboard focus', async () => {
    render(
      <ChannelScoreBadge
        summary={summary({
          scored: true,
          multiplier: 1.2,
          source: 'probe',
          entries: [
            {
              group: '',
              model: 'probe-model',
              source: 'probe',
              effectiveSource: 'probe',
              probeModel: 'probe-model',
              scored: true,
              multiplier: 1.2,
              avgFirstByteMs: 800,
              requests: 10,
              serverErrors: 0,
            },
          ],
        })}
        probeEnabled
        minSamples={10}
      />
    )

    await userEvent.tab()
    expect(
      screen.getByLabelText('Adaptive score: ×1.20 Baseline')
    ).toHaveFocus()
    expect(
      await screen.findByText(
        /Baseline probe-model: ×1.20 · 800ms · 10\/10 samples/
      )
    ).toBeVisible()
  })
})
