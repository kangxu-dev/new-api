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
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

import { SettingsSwitchField } from '../components/settings-form-layout'
import type { RoutingPolicyFormValues } from './routing-form'

const PROBE_SCHEDULE_DAYS = [
  { day: 1, label: 'Monday' },
  { day: 2, label: 'Tuesday' },
  { day: 3, label: 'Wednesday' },
  { day: 4, label: 'Thursday' },
  { day: 5, label: 'Friday' },
  { day: 6, label: 'Saturday' },
  { day: 7, label: 'Sunday' },
] as const

/**
 * Weekly restriction for automatic probe dispatch. The seven weekdays are
 * rendered as an always-visible toggle group: MultiSelect's searchable dropdown
 * and create-value affordances do not fit a fixed seven-day domain.
 */
export function ProbeScheduleFields() {
  const { t } = useTranslation()
  const form = useFormContext<RoutingPolicyFormValues>()
  const scheduleEnabled = useWatch({
    control: form.control,
    name: 'adaptive_channel_routing_setting.probe_schedule_enabled',
  })

  return (
    <div className='space-y-4 rounded-lg border p-4'>
      <FormField
        control={form.control}
        name='adaptive_channel_routing_setting.probe_schedule_enabled'
        render={({ field }) => (
          <SettingsSwitchField
            controlId='adaptive_channel_routing_setting.probe_schedule_enabled'
            checked={field.value}
            onCheckedChange={field.onChange}
            label={t('Limit probe sampling to a weekly schedule')}
            description={t(
              'Only sends automatic probe requests inside the selected Beijing-time window.'
            )}
          />
        )}
      />
      {scheduleEnabled ? (
        <>
          <FormField
            control={form.control}
            name='adaptive_channel_routing_setting.probe_schedule_days'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Probe sampling days')}</FormLabel>
                <FormControl>
                  <ToggleGroup
                    multiple
                    variant='outline'
                    size='sm'
                    className='flex-wrap'
                    aria-label={t('Probe sampling days')}
                    value={field.value.map(String)}
                    onValueChange={(values) =>
                      field.onChange(
                        values.map(Number).sort((left, right) => left - right)
                      )
                    }
                  >
                    {PROBE_SCHEDULE_DAYS.map((weekday) => (
                      <ToggleGroupItem
                        key={weekday.day}
                        value={String(weekday.day)}
                      >
                        {t(weekday.label)}
                      </ToggleGroupItem>
                    ))}
                  </ToggleGroup>
                </FormControl>
                <FormDescription>
                  {t(
                    'Times use Asia/Shanghai (UTC+8). The end time is exclusive; after-midnight hours belong to the start day.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='adaptive_channel_routing_setting.probe_schedule_start'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Start time')}</FormLabel>
                  <FormControl>
                    <Input type='time' {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='adaptive_channel_routing_setting.probe_schedule_end'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('End time')}</FormLabel>
                  <FormControl>
                    <Input type='time' {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
        </>
      ) : null}
    </div>
  )
}
