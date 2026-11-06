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
import { AlertTriangle, RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatDateTimeStr, formatDuration, formatNumber } from '@/lib/format'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { getChannelQuota } from '../../api'
import {
  CHANNEL_QUOTA_ERROR_KEYS,
  CHANNEL_QUOTA_WINDOW_LABEL_KEYS,
} from '../../lib/channel-quota'
import type {
  ChannelQuota,
  ChannelQuotaExtras,
  ChannelQuotaResponse,
  ChannelQuotaWindow,
} from '../../types'
import { useChannels } from '../channels-provider'

type ChannelQuotaDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

const WARNING_PERCENT = 80
const FULL_PERCENT = 100
const EMPTY_VALUE = '—'
const COUNTDOWN_TICK_MS = 30_000

function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(FULL_PERCENT, Math.max(0, value))
}

function isFiniteNumber(value: number | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function getPercentClassNames(percent: number): {
  text: string
  progress: string
} {
  if (percent >= FULL_PERCENT) {
    return {
      text: 'text-destructive',
      progress: '[&_[data-slot=progress-indicator]]:bg-destructive',
    }
  }
  if (percent >= WARNING_PERCENT) {
    return {
      text: 'text-warning',
      progress: '[&_[data-slot=progress-indicator]]:bg-warning',
    }
  }
  return { text: 'text-muted-foreground', progress: '' }
}

function formatResetCountdown(
  resetsAt: number | undefined,
  now: number
): string | null {
  if (!resetsAt) return null
  const seconds = (resetsAt - now) / 1000
  return seconds > 0 ? formatDuration(seconds) : null
}

function QuotaWindowRow(props: {
  quotaWindow: ChannelQuotaWindow
  now: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const percent = clampPercent(props.quotaWindow.percent)
  const percentLabel = `${formatNumber(percent, locale)}%`
  const windowLabelKey = CHANNEL_QUOTA_WINDOW_LABEL_KEYS[props.quotaWindow.kind]
  const name = windowLabelKey ? t(windowLabelKey) : props.quotaWindow.name
  const classes = getPercentClassNames(percent)
  const hasUsedCap =
    isFiniteNumber(props.quotaWindow.used) &&
    isFiniteNumber(props.quotaWindow.cap)

  let countdownLabel = EMPTY_VALUE
  let resetAtLabel = EMPTY_VALUE
  if (props.quotaWindow.resetsAt) {
    if (props.quotaWindow.resetsAt > props.now) {
      countdownLabel =
        formatResetCountdown(props.quotaWindow.resetsAt, props.now) ??
        EMPTY_VALUE
      resetAtLabel = formatDateTimeStr(new Date(props.quotaWindow.resetsAt))
    } else {
      countdownLabel = t('Resetting...')
    }
  }

  return (
    <div className='rounded-lg border p-3'>
      <div className='flex items-baseline justify-between gap-2'>
        <span className='text-sm font-medium'>{name}</span>
        <span
          className={cn('text-sm font-semibold tabular-nums', classes.text)}
        >
          {percentLabel}
        </span>
      </div>
      <Progress
        value={percent}
        aria-label={`${name}: ${percentLabel}`}
        className={cn('mt-2', classes.progress)}
      />
      <div className='text-muted-foreground mt-2 flex flex-wrap items-baseline gap-x-4 gap-y-1 text-xs'>
        {hasUsedCap ? (
          <span className='tabular-nums'>
            {formatNumber(props.quotaWindow.used, locale)} /{' '}
            {formatNumber(props.quotaWindow.cap, locale)} {t('credits')}
          </span>
        ) : null}
        <span className='tabular-nums'>
          {t('Resets in:')} {countdownLabel}
        </span>
        <span className='tabular-nums'>
          {t('Reset at:')} {resetAtLabel}
        </span>
      </div>
    </div>
  )
}

function QuotaExtrasSection(props: { extras: ChannelQuotaExtras }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const fields: { label: string; value: string }[] = []

  if (isFiniteNumber(props.extras.monthlyCredits)) {
    fields.push({
      label: t('Credit remaining'),
      value: formatNumber(props.extras.monthlyCredits, locale),
    })
  }
  if (isFiniteNumber(props.extras.monthlySpent)) {
    fields.push({
      label: t('Monthly spent'),
      value: formatNumber(props.extras.monthlySpent, locale),
    })
  }
  const billingPeriodEnd = props.extras.billingPeriodEnd
    ? dayjs(props.extras.billingPeriodEnd)
    : null
  if (billingPeriodEnd?.isValid()) {
    fields.push({
      label: t('Billing period ends'),
      value: formatDateTimeStr(billingPeriodEnd.toDate()),
    })
  }
  if (fields.length === 0) return null

  return (
    <div className='bg-muted/30 rounded-lg border p-3'>
      <div className='grid grid-cols-1 gap-2 sm:grid-cols-3'>
        {fields.map((field) => (
          <div key={field.label}>
            <div className='text-muted-foreground text-[11px]'>
              {field.label}
            </div>
            <div className='mt-1 text-sm font-medium tabular-nums'>
              {field.value}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

function QuotaDialogSkeleton() {
  return (
    <div className='flex flex-col gap-3'>
      <Skeleton className='h-24 w-full' />
      <Skeleton className='h-20 w-full' />
      <Skeleton className='h-20 w-full' />
      <Skeleton className='h-20 w-full' />
    </div>
  )
}

export function ChannelQuotaDialog(props: ChannelQuotaDialogProps) {
  const { t } = useTranslation()
  const { currentRow } = useChannels()
  const [response, setResponse] = useState<ChannelQuotaResponse | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const requestRef = useRef<AbortController | null>(null)

  const channelId = currentRow?.id ?? null

  const load = useCallback(async () => {
    if (channelId === null) return
    requestRef.current?.abort()
    const controller = new AbortController()
    requestRef.current = controller
    setIsLoading(true)
    setErrorMessage(null)
    try {
      const result = await getChannelQuota(channelId, controller.signal)
      if (controller.signal.aborted) return
      setResponse(result)
      setNow(Date.now())
    } catch (error) {
      if (controller.signal.aborted) return
      setResponse(null)
      setErrorMessage(
        getServerErrorMessage(error, t('Failed to fetch upstream quota'))
      )
    } finally {
      if (!controller.signal.aborted) setIsLoading(false)
    }
  }, [channelId, t])

  useEffect(() => {
    if (!props.open) return
    void load()
    return () => {
      requestRef.current?.abort()
    }
  }, [props.open, load])

  useEffect(() => {
    if (props.open) return
    requestRef.current?.abort()
    setResponse(null)
    setErrorMessage(null)
    setIsLoading(false)
  }, [props.open])

  const quota: ChannelQuota | null =
    response?.success === true ? (response.data ?? null) : null

  useEffect(() => {
    if (!props.open || !quota) return
    const timer = window.setInterval(
      () => setNow(Date.now()),
      COUNTDOWN_TICK_MS
    )
    return () => window.clearInterval(timer)
  }, [props.open, quota])

  const failedCode = response && !response.success ? response.code : undefined
  let errorTitle = errorMessage
  if (!errorTitle && response && !response.success) {
    const errorKey = failedCode
      ? CHANNEL_QUOTA_ERROR_KEYS[failedCode]
      : undefined
    errorTitle = errorKey
      ? t(errorKey)
      : response.message?.trim() || t('Failed to fetch upstream quota')
  }

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) requestRef.current?.abort()
    props.onOpenChange(nextOpen)
  }

  if (!props.open || !currentRow) return null

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleOpenChange}
      title={t('Upstream Quota')}
      description={t('Upstream quota windows for this channel.')}
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => void load()}
            disabled={isLoading}
          >
            <RefreshCw
              data-icon='inline-start'
              className={cn(isLoading && 'animate-spin')}
            />
            {t('Refresh')}
          </Button>
          <Button
            type='button'
            variant='outline'
            onClick={() => handleOpenChange(false)}
          >
            {t('Close')}
          </Button>
        </>
      }
    >
      {isLoading && !quota && !errorTitle ? <QuotaDialogSkeleton /> : null}

      {errorTitle ? (
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{errorTitle}</AlertTitle>
          {failedCode === 'provider_unsupported' ? (
            <AlertDescription>
              {t('Only Command Code and OpenCode Go channels are supported.')}
            </AlertDescription>
          ) : null}
        </Alert>
      ) : null}

      {quota ? (
        <>
          <div className='bg-muted/30 rounded-lg border p-4'>
            <div className='grid grid-cols-1 gap-3 sm:grid-cols-3'>
              <div className='min-w-0'>
                <div className='text-muted-foreground text-[11px]'>
                  {t('Provider')}
                </div>
                <div className='mt-1 text-sm font-medium break-words'>
                  {quota.providerName}
                </div>
              </div>
              <div className='min-w-0'>
                <div className='text-muted-foreground text-[11px]'>
                  {t('Channel')}
                </div>
                <div className='mt-1 text-sm font-medium break-words'>
                  {quota.channelName}
                </div>
              </div>
              {quota.plan ? (
                <div className='min-w-0'>
                  <div className='text-muted-foreground text-[11px]'>
                    {t('Plan')}
                  </div>
                  <div className='mt-1 font-mono text-sm font-medium break-all'>
                    {quota.plan}
                  </div>
                </div>
              ) : null}
            </div>
            <div className='text-muted-foreground mt-3 text-[11px] tabular-nums'>
              {t('Last updated:')}{' '}
              {formatDateTimeStr(new Date(quota.fetchedAt))}
            </div>
          </div>

          {quota.warnings && quota.warnings.length > 0 ? (
            <Alert>
              <AlertTriangle />
              <AlertTitle>{t('Upstream warnings')}</AlertTitle>
              <AlertDescription>
                <ul className='list-disc space-y-1 ps-4'>
                  {quota.warnings.map((warning) => (
                    <li key={warning}>{warning}</li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          ) : null}

          <div className='flex flex-col gap-3'>
            {quota.windows.map((quotaWindow) => (
              <QuotaWindowRow
                key={quotaWindow.kind}
                quotaWindow={quotaWindow}
                now={now}
              />
            ))}
          </div>

          {quota.extras ? <QuotaExtrasSection extras={quota.extras} /> : null}
        </>
      ) : null}
    </Dialog>
  )
}
