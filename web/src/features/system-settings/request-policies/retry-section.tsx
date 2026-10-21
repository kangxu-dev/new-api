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
  const sameChannelRetries = useWatch({
    control: form.control,
    name: 'retry_failover_setting.same_channel_retries',
  })
  const cooldownSeconds = useWatch({
    control: form.control,
    name: 'retry_failover_setting.cooldown_seconds',
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
        title={t('Channel failover')}
        description={t(
          'How a failed attempt moves to another channel instead of repeating the one that just failed.'
        )}
        className='shadow-none'
      >
        <div className='space-y-4'>
          <FormField
            control={form.control}
            name='retry_failover_setting.exclude_failed_channels'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='retry_failover_setting.exclude_failed_channels'
                checked={field.value}
                onCheckedChange={field.onChange}
                label={t('Never retry on a channel that already failed')}
                description={t(
                  'Keeps a retry from re-selecting a channel that failed earlier in the same request.'
                )}
              />
            )}
          />
          <FormField
            control={form.control}
            name='retry_failover_setting.stop_after_response_written'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='retry_failover_setting.stop_after_response_written'
                checked={field.value}
                onCheckedChange={field.onChange}
                label={t('Stop retrying once the response has started')}
                description={t(
                  'Switching channels after bytes reached the client would append a second stream to the same response. SSE keepalives do not count.'
                )}
              />
            )}
          />
          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='retry_failover_setting.same_channel_retries'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Same-channel retries before switching')}
                  </FormLabel>
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
                    {t(
                      'Transient upstream errors retry in place first. Zero switches channel immediately.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='retry_failover_setting.same_channel_status_codes'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Same-channel retry status codes')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      disabled={sameChannelRetries === 0}
                      placeholder={t('e.g. 429, 500-503, 529')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
          {sameChannelRetries > 0 ? (
            <div className='grid gap-4 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='retry_failover_setting.backoff_ms'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Retry backoff (ms)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={60000}
                        step={100}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Doubles after every retry.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='retry_failover_setting.backoff_max_ms'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Retry backoff cap (ms)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={600000}
                        step={100}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          ) : null}
          <FormField
            control={form.control}
            name='retry_failover_setting.selection_exhausted_backoff_ms'
            render={({ field }) => (
              <FormItem>
                <FormLabel>
                  {t('Wait before reusing an exhausted channel pool (ms)')}
                </FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={0}
                    max={60000}
                    step={100}
                    {...safeNumberFieldProps(field)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'After an upstream 503 empties the candidate pool, wait this long, clear the exclusion list and select again.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </div>
      </SettingsCard>
      <SettingsCard
        title={t('Channel cooldown')}
        description={t(
          'Removes a failing channel from selection for a while instead of disabling it permanently.'
        )}
        className='shadow-none'
      >
        <div className='space-y-4'>
          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='retry_failover_setting.cooldown_seconds'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Cooldown duration (seconds)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={0}
                      max={86400}
                      step={10}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Zero disables cooldowns.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='retry_failover_setting.cooldown_status_codes'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Cooldown status codes')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      disabled={cooldownSeconds === 0}
                      placeholder={t('e.g. 429, 500-503, 529')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
          {cooldownSeconds > 0 ? (
            <p className='text-muted-foreground text-sm'>
              {t(
                'While a cooldown is active, these status codes no longer disable the channel permanently. Codes outside this range keep the existing automatic-disable behavior.'
              )}
            </p>
          ) : null}
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
