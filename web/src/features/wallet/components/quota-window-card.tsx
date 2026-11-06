import { useQuery } from '@tanstack/react-query'
import { Gauge } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { QuotaWindowUsage } from '@/components/quota-window-usage'
import { IconBadge } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { getUsageWindowSummary } from '@/features/usage-logs/api'
import { exhaustEtaLabel } from '@/features/usage-logs/lib/window-usage'
import type { QuotaWindowOverview } from '@/features/users/types'
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
        {windows.map((window) => {
          const stat = summaryByKind.get(window.kind)
          const eta = stat
            ? exhaustEtaLabel(stat.estimated_exhaust_seconds)
            : null
          return (
            <div key={window.kind} className='rounded-md border p-3'>
              <div className='text-muted-foreground text-xs font-medium uppercase'>
                {windowLabel(window.kind, t)}
              </div>
              <QuotaWindowUsage
                className='mt-1'
                limit={window.limit}
                resetAt={window.reset_at}
                serverTime={data?.server_time ?? 0}
                used={window.used}
                variant='card'
              />
              {window.limit > 0 && window.used >= window.limit && (
                <div className='text-destructive mt-1 text-xs font-medium'>
                  {t('Limit reached')}
                </div>
              )}
              {window.limit > 0 && window.used < window.limit && eta && (
                <div className='text-muted-foreground mt-1 text-xs'>
                  {t('Estimated limit in {{duration}}', { duration: eta })}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
