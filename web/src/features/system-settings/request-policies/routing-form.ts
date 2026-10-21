import type { TFunction } from 'i18next'
import { z } from 'zod'

import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

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
    adaptive_channel_routing_setting: z.object({
      enabled: z.boolean(),
      window_minutes: z.number().int().min(1).max(60),
      min_samples: z.number().int().min(1).max(10000),
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
      backoff_ms: Number(
        options['retry_failover_setting.backoff_ms'] || 500
      ),
      backoff_max_ms: Number(
        options['retry_failover_setting.backoff_max_ms'] || 8000
      ),
      selection_exhausted_backoff_ms: Number(
        options['retry_failover_setting.selection_exhausted_backoff_ms'] ||
          2000
      ),
      cooldown_seconds: Number(
        options['retry_failover_setting.cooldown_seconds'] || 0
      ),
      cooldown_status_codes:
        options['retry_failover_setting.cooldown_status_codes'] ||
        '429,500-503,529',
    },
    adaptive_channel_routing_setting: {
      enabled:
        options['adaptive_channel_routing_setting.enabled'] === 'true',
      window_minutes: Number(
        options['adaptive_channel_routing_setting.window_minutes'] || 30
      ),
      min_samples: Number(
        options['adaptive_channel_routing_setting.min_samples'] || 10
      ),
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
        ([key, value]) => [`adaptive_channel_routing_setting.${key}`, String(value)]
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
