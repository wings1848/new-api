import { useQuery } from '@tanstack/react-query'
import dayjs from 'dayjs'
import { Gauge } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { getUsageWindowSummary } from '@/features/usage-logs/api'
import { exhaustEtaLabel } from '@/features/usage-logs/lib/window-usage'
import type { QuotaWindowOverview } from '@/features/users/types'
import { formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getQuotaWindowSelf } from '../api'

function windowLabel(kind: string, t: (key: string) => string): string {
  if (kind === '5h') {
    return t('5-hour window')
  }
  return t('7-day window')
}

export function QuotaWindowCard() {
  const { t } = useTranslation()
  const { data, isLoading } = useQuery({
    queryKey: ['quota-window-self'],
    queryFn: async (): Promise<QuotaWindowOverview | null> => {
      const response = await getQuotaWindowSelf()
      requireServerSuccess(response)
      return response.data ?? null
    },
    staleTime: 30 * 1000,
  })
  const { data: summary } = useQuery({
    queryKey: ['usage-window-summary'],
    queryFn: () => getUsageWindowSummary(),
    staleTime: 30 * 1000,
  })
  const summaryByKind = new Map(
    (summary?.windows ?? []).map((window) => [window.kind, window])
  )

  if (isLoading) {
    return (
      <div className='rounded-lg border p-4'>
        <Skeleton className='h-4 w-40' />
        <Skeleton className='mt-3 h-6 w-full' />
      </div>
    )
  }

  const windows = data?.windows ?? []
  const configured = windows.some((window) => window.limit > 0)
  if (!configured) {
    return (
      <div className='text-muted-foreground flex items-center gap-2 rounded-lg border p-4 text-sm'>
        <Gauge className='size-4' />
        {t('No quota window limit configured.')}
      </div>
    )
  }

  return (
    <div className='rounded-lg border p-4'>
      <div className='flex items-center gap-2'>
        <IconBadge tone='info' size='stat'>
          <Gauge />
        </IconBadge>
        <h3 className='text-sm font-medium'>{t('Quota Windows')}</h3>
      </div>
      <div className='mt-3 grid gap-3 sm:grid-cols-2'>
        {windows.map((window) => (
          <div key={window.kind} className='rounded-md border p-3'>
            <div className='text-muted-foreground text-xs font-medium uppercase'>
              {windowLabel(window.kind, t)}
            </div>
            <div className='mt-1 font-mono text-lg font-bold tabular-nums'>
              {formatQuota(window.remaining)}
            </div>
            <div className='text-muted-foreground mt-1 flex items-baseline gap-1 text-xs'>
              <span>{t('Remaining')}</span>
              <span>·</span>
              <span>{t('Resets at')}</span>
              <span>{dayjs.unix(window.reset_at).format('MM-DD HH:mm')}</span>
            </div>
            {(() => {
              const stat = summaryByKind.get(window.kind)
              if (!stat || stat.limit <= 0) {
                return null
              }
              if (stat.used >= stat.limit) {
                return (
                  <div className='text-destructive mt-1 text-xs font-medium'>
                    {t('Limit reached')}
                  </div>
                )
              }
              const eta = exhaustEtaLabel(stat.estimated_exhaust_seconds)
              if (!eta) {
                return null
              }
              return (
                <div className='text-muted-foreground mt-1 text-xs'>
                  {t('Estimated limit in {{duration}}', { duration: eta })}
                </div>
              )
            })()}
          </div>
        ))}
      </div>
    </div>
  )
}
