import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'

import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { formatDuration, formatNumber, formatQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

const WARNING_PERCENT = 80
const FULL_PERCENT = 100

export interface QuotaWindowUsageProps {
  used: number
  limit: number
  resetAt: number
  serverTime: number
  /**
   * `card` renders a stacked meter with a progress bar for the wallet card;
   * `inline` renders the single summary line used by the admin user drawer.
   */
  variant?: 'card' | 'inline'
  className?: string
}

function usagePercent(used: number, limit: number): number {
  if (limit <= 0 || !Number.isFinite(used) || !Number.isFinite(limit)) {
    return 0
  }
  return Math.min(FULL_PERCENT, Math.max(0, Math.round((used / limit) * 100)))
}

/** Seconds until the window resets, relative to the server clock. */
function resetInSeconds(resetAt: number, serverTime: number): number | null {
  if (resetAt <= 0 || serverTime <= 0) {
    return null
  }
  const seconds = resetAt - serverTime
  return seconds > 0 ? seconds : null
}

export function QuotaWindowUsage(props: QuotaWindowUsageProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const unlimited = props.limit <= 0
  const percent = unlimited ? null : usagePercent(props.used, props.limit)
  const percentLabel =
    percent === null ? null : `${formatNumber(percent, locale)}%`
  const resetIn = resetInSeconds(props.resetAt, props.serverTime)
  const resetLabel = resetIn === null ? null : formatDuration(resetIn)

  let percentClass = 'text-muted-foreground'
  let progressClass = ''
  if (percent !== null && percent >= FULL_PERCENT) {
    percentClass = 'text-destructive'
    progressClass = '[&_[data-slot=progress-indicator]]:bg-destructive'
  } else if (percent !== null && percent >= WARNING_PERCENT) {
    percentClass = 'text-warning'
    progressClass = '[&_[data-slot=progress-indicator]]:bg-warning'
  }

  if (props.variant === 'inline') {
    let usageLabel = `${t('Used:')} ${formatQuota(props.used)} · ${t('Unlimited')}`
    if (!unlimited) {
      usageLabel = t('Used {{used}} of {{total}} ({{percent}}%)', {
        used: formatQuota(props.used),
        total: formatQuota(props.limit),
        percent: formatNumber(percent ?? 0, locale),
      })
    }
    return (
      <p className={cn('text-muted-foreground text-xs', props.className)}>
        {usageLabel}
        {resetLabel !== null && (
          <>
            {' · '}
            {t('Reset in {{duration}}', { duration: resetLabel })}
          </>
        )}
      </p>
    )
  }

  return (
    <div className={cn('min-w-0', props.className)}>
      <div className='flex items-baseline justify-between gap-2'>
        <span className='font-mono text-lg font-bold tabular-nums'>
          {unlimited
            ? t('Unlimited')
            : `${formatQuota(props.used)} / ${formatQuota(props.limit)}`}
        </span>
        {percentLabel !== null && (
          <span
            className={cn('text-xs font-medium tabular-nums', percentClass)}
          >
            {percentLabel}
          </span>
        )}
      </div>
      {percent !== null && percentLabel !== null && (
        <Progress
          value={percent}
          aria-label={t('Used {{used}} of {{total}} ({{percent}}%)', {
            used: formatQuota(props.used),
            total: formatQuota(props.limit),
            percent: formatNumber(percent, locale),
          })}
          className={cn('mt-2', progressClass)}
        />
      )}
      <div className='text-muted-foreground mt-1 text-xs'>
        {unlimited
          ? `${t('Used:')} ${formatQuota(props.used)}`
          : `${t('Remaining')} ${formatQuota(
              Math.max(0, props.limit - props.used)
            )}`}
        {resetLabel !== null && (
          <>
            {' · '}
            {t('Reset in {{duration}}', { duration: resetLabel })}
          </>
        )}
      </div>
      {props.resetAt > 0 && (
        <div className='text-muted-foreground mt-0.5 text-xs'>
          {t('Reset at:')}{' '}
          {dayjs.unix(props.resetAt).format('MM-DD HH:mm')}
        </div>
      )}
    </div>
  )
}
