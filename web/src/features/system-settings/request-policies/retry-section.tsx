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
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { SettingsCard } from '../components/settings-card'
import { SettingsSwitchField } from '../components/settings-form-layout'
import { safeNumberFieldProps } from '../utils/numeric-field'
import type { RoutingPolicyFormValues } from './routing-form'

export function RetrySection() {
  const { t } = useTranslation()
  const form = useFormContext<RoutingPolicyFormValues>()
  const adaptiveEnabled = useWatch({
    control: form.control,
    name: 'adaptive_channel_routing_setting.enabled',
  })
  return (
    <div className='space-y-4'>
      <SettingsCard title={t('Retry budget')} className='shadow-none'>
        <div className='space-y-4'>
          <FormField
            control={form.control}
            name='RetryTimes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Maximum retries')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={0}
                    max={99}
                    step={1}
                    {...safeNumberFieldProps(field)}
                  />
                </FormControl>
                <FormDescription>
                  {t('Excludes the first attempt. Counted per group.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='AutomaticRetryStatusCodes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Auto-retry status codes')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    placeholder={t('e.g. 401, 403, 429, 500-599')}
                  />
                </FormControl>
                <FormDescription>
                  {t('2xx, 504 and 524 are always excluded.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </div>
      </SettingsCard>
      <SettingsCard
        title={t('Adaptive channel scoring')}
        description={t(
          'Scores same-priority channels by recent success rate, upstream 5xx rate, and first-byte latency.'
        )}
        className='shadow-none'
      >
        <div className='space-y-4'>
          <FormField
            control={form.control}
            name='adaptive_channel_routing_setting.enabled'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='adaptive_channel_routing_setting.enabled'
                checked={field.value}
                onCheckedChange={field.onChange}
                label={t('Use adaptive scores for same-priority channels')}
                description={t(
                  'Disabled by default. Channels with fewer than the minimum samples keep their configured weight.'
                )}
              />
            )}
          />
          {adaptiveEnabled ? (
            <div className='grid gap-4 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='adaptive_channel_routing_setting.window_minutes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Scoring window (minutes)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={60}
                        step={1}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='adaptive_channel_routing_setting.min_samples'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Minimum samples per channel')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={10000}
                        step={1}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          ) : null}
        </div>
      </SettingsCard>
    </div>
  )
}
