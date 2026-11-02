import type { TFunction } from 'i18next'
import { z } from 'zod'

import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

const PROBE_SCHEDULE_CLOCK_PATTERN = /^([01]\d|2[0-3]):[0-5]\d$/

export function createRoutingPolicySchema(t: TFunction) {
  return z.object({
    RetryTimes: z.number().int().min(0).max(99),
    AutomaticRetryStatusCodes: z
      .string()
      .refine(
        (value) => parseHttpStatusCodeRules(value).ok,
        t('Invalid status code rules')
      ),
    retry_failover_setting: z.object({
      exclude_failed_channels: z.boolean(),
      stop_after_response_written: z.boolean(),
      same_channel_retries: z.number().int().min(0).max(99),
      same_channel_status_codes: z
        .string()
        .refine(
          (value) => parseHttpStatusCodeRules(value).ok,
          t('Invalid status code rules')
        ),
      backoff_ms: z.number().int().min(0).max(60000),
      backoff_max_ms: z.number().int().min(0).max(600000),
      selection_exhausted_backoff_ms: z.number().int().min(0).max(60000),
      cooldown_seconds: z.number().int().min(0).max(86400),
      cooldown_status_codes: z
        .string()
        .refine(
          (value) => parseHttpStatusCodeRules(value).ok,
          t('Invalid status code rules')
        ),
    }),
    adaptive_channel_routing_setting: z
      .object({
        enabled: z.boolean(),
        window_minutes: z.number().int().min(1).max(60),
        min_samples: z.number().int().min(1).max(10000),
        latency_mode: z.enum(['absolute', 'ratio']),
        latency_exponent: z.number().min(0.1).max(10),
        multiplier_min: z.number().min(0).max(1),
        multiplier_max: z.number().min(1).max(10),
        probe_enabled: z.boolean(),
        probe_model: z
          .string()
          .trim()
          .max(128)
          .refine(
            (value) => !/\p{Cc}/u.test(value),
            t('Probe model must not contain control characters')
          ),
        probe_interval_seconds: z.number().int().min(30).max(3600),
        probe_max_requests: z.number().int().min(1).max(100),
        probe_max_output_tokens: z.number().int().min(1).max(1024),
        probe_schedule_enabled: z.boolean(),
        probe_schedule_days: z.array(z.number().int().min(1).max(7)),
        probe_schedule_start: z.string(),
        probe_schedule_end: z.string(),
      })
      .superRefine((value, context) => {
        if (value.probe_enabled && value.probe_model.trim() === '') {
          context.addIssue({
            code: 'custom',
            path: ['probe_model'],
            message: t(
              'Probe model is required when probe sampling is enabled'
            ),
          })
        }
        if (!value.probe_schedule_enabled) {
          for (const [field, clock] of [
            ['probe_schedule_start', value.probe_schedule_start],
            ['probe_schedule_end', value.probe_schedule_end],
          ] as const) {
            if (clock !== '' && !PROBE_SCHEDULE_CLOCK_PATTERN.test(clock)) {
              context.addIssue({
                code: 'custom',
                path: [field],
                message: t('Use 24-hour time in HH:mm format'),
              })
            }
          }
          return
        }
        if (value.probe_schedule_days.length === 0) {
          context.addIssue({
            code: 'custom',
            path: ['probe_schedule_days'],
            message: t('Select at least one day'),
          })
        }
        if (
          new Set(value.probe_schedule_days).size !==
          value.probe_schedule_days.length
        ) {
          context.addIssue({
            code: 'custom',
            path: ['probe_schedule_days'],
            message: t('A day can only be selected once'),
          })
        }
        for (const [field, clock] of [
          ['probe_schedule_start', value.probe_schedule_start],
          ['probe_schedule_end', value.probe_schedule_end],
        ] as const) {
          if (clock === '') {
            context.addIssue({
              code: 'custom',
              path: [field],
              message: t('Start and end times are required'),
            })
          } else if (!PROBE_SCHEDULE_CLOCK_PATTERN.test(clock)) {
            context.addIssue({
              code: 'custom',
              path: [field],
              message: t('Use 24-hour time in HH:mm format'),
            })
          }
        }
        if (
          value.probe_schedule_start !== '' &&
          value.probe_schedule_end !== '' &&
          value.probe_schedule_start === value.probe_schedule_end
        ) {
          context.addIssue({
            code: 'custom',
            path: ['probe_schedule_start'],
            message: t('Start and end times must differ'),
          })
        }
      }),
    channel_affinity_setting: z.object({
      enabled: z.boolean(),
      session_mode: z.enum(['', 'off', 'prefer', 'strict']),
      switch_on_success: z.boolean(),
      keep_on_channel_disabled: z.boolean(),
      max_entries: z.number().int().min(0),
      default_ttl_seconds: z.number().int().min(0),
      rules: z.string().superRefine((value, context) => {
        try {
          const rules: unknown = JSON.parse(value)
          if (!Array.isArray(rules)) {
            context.addIssue({
              code: 'custom',
              message: t('Rules JSON must be an array'),
            })
          }
        } catch {
          context.addIssue({
            code: 'custom',
            message: t('Invalid rules JSON format'),
          })
        }
      }),
    }),
  })
}

export type RoutingPolicyFormValues = z.infer<
  ReturnType<typeof createRoutingPolicySchema>
>

function parseProbeScheduleDays(value: string | undefined): number[] {
  if (value === undefined) {
    return [1, 2, 3, 4, 5, 6, 7]
  }
  try {
    const parsed: unknown = JSON.parse(value)
    if (
      Array.isArray(parsed) &&
      parsed.every((day) => Number.isInteger(day) && day >= 1 && day <= 7)
    ) {
      return parsed as number[]
    }
  } catch {
    // Invalid stored JSON stays visible as an empty selection and cannot be
    // saved as an unrestricted schedule while the restriction is enabled.
  }
  return []
}

export function routingPolicyFormValues(
  options: Record<string, string>
): RoutingPolicyFormValues {
  return {
    RetryTimes: Number(options.RetryTimes),
    AutomaticRetryStatusCodes: options.AutomaticRetryStatusCodes,
    retry_failover_setting: {
      exclude_failed_channels:
        options['retry_failover_setting.exclude_failed_channels'] !== 'false',
      stop_after_response_written:
        options['retry_failover_setting.stop_after_response_written'] !==
        'false',
      same_channel_retries: Number(
        options['retry_failover_setting.same_channel_retries'] || 0
      ),
      same_channel_status_codes:
        options['retry_failover_setting.same_channel_status_codes'] ||
        '429,500,502,503,529',
      backoff_ms: Number(options['retry_failover_setting.backoff_ms'] || 500),
      backoff_max_ms: Number(
        options['retry_failover_setting.backoff_max_ms'] || 8000
      ),
      selection_exhausted_backoff_ms: Number(
        options['retry_failover_setting.selection_exhausted_backoff_ms'] || 2000
      ),
      cooldown_seconds: Number(
        options['retry_failover_setting.cooldown_seconds'] || 0
      ),
      cooldown_status_codes:
        options['retry_failover_setting.cooldown_status_codes'] ||
        '429,500-503,529',
    },
    adaptive_channel_routing_setting: {
      enabled: options['adaptive_channel_routing_setting.enabled'] === 'true',
      window_minutes: Number(
        options['adaptive_channel_routing_setting.window_minutes'] || 30
      ),
      min_samples: Number(
        options['adaptive_channel_routing_setting.min_samples'] || 10
      ),
      latency_mode:
        options['adaptive_channel_routing_setting.latency_mode'] === 'ratio'
          ? 'ratio'
          : 'absolute',
      latency_exponent: Number(
        options['adaptive_channel_routing_setting.latency_exponent'] || 2
      ),
      multiplier_min: Number(
        options['adaptive_channel_routing_setting.multiplier_min'] || 0.5
      ),
      multiplier_max: Number(
        options['adaptive_channel_routing_setting.multiplier_max'] || 1.5
      ),
      probe_enabled:
        options['adaptive_channel_routing_setting.probe_enabled'] === 'true',
      probe_model:
        options['adaptive_channel_routing_setting.probe_model'] || '',
      probe_interval_seconds: Number(
        options['adaptive_channel_routing_setting.probe_interval_seconds'] || 60
      ),
      probe_max_requests: Number(
        options['adaptive_channel_routing_setting.probe_max_requests'] || 6
      ),
      probe_max_output_tokens: Number(
        options['adaptive_channel_routing_setting.probe_max_output_tokens'] ||
          16
      ),
      probe_schedule_enabled:
        options['adaptive_channel_routing_setting.probe_schedule_enabled'] ===
        'true',
      probe_schedule_days: parseProbeScheduleDays(
        options['adaptive_channel_routing_setting.probe_schedule_days']
      ),
      probe_schedule_start:
        options['adaptive_channel_routing_setting.probe_schedule_start'] ??
        '09:00',
      probe_schedule_end:
        options['adaptive_channel_routing_setting.probe_schedule_end'] ??
        '23:00',
    },
    channel_affinity_setting: {
      enabled: options['channel_affinity_setting.enabled'] === 'true',
      session_mode: (options['channel_affinity_setting.session_mode'] ||
        '') as RoutingPolicyFormValues['channel_affinity_setting']['session_mode'],
      switch_on_success:
        options['channel_affinity_setting.switch_on_success'] === 'true',
      keep_on_channel_disabled:
        options['channel_affinity_setting.keep_on_channel_disabled'] === 'true',
      max_entries: Number(options['channel_affinity_setting.max_entries']),
      default_ttl_seconds: Number(
        options['channel_affinity_setting.default_ttl_seconds']
      ),
      rules: options['channel_affinity_setting.rules'] || '[]',
    },
  }
}

export function routingPolicyOptions(
  values: RoutingPolicyFormValues
): Record<string, string> {
  return {
    RetryTimes: String(values.RetryTimes),
    AutomaticRetryStatusCodes: values.AutomaticRetryStatusCodes,
    ...Object.fromEntries(
      Object.entries(values.retry_failover_setting).map(([key, value]) => [
        `retry_failover_setting.${key}`,
        String(value),
      ])
    ),
    ...Object.fromEntries(
      Object.entries(values.adaptive_channel_routing_setting).map(
        ([key, value]) => [
          `adaptive_channel_routing_setting.${key}`,
          key === 'probe_schedule_days' ? JSON.stringify(value) : String(value),
        ]
      )
    ),
    ...Object.fromEntries(
      Object.entries(values.channel_affinity_setting).map(([key, value]) => [
        `channel_affinity_setting.${key}`,
        String(value),
      ])
    ),
  }
}
