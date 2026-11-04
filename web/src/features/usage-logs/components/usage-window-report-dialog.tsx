import { useQuery } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import {
  formatLogQuota,
  formatTimestampToDate,
  formatTokens,
} from '@/lib/format'

import { getUsageWindowReport } from '../api'
import { windowKindLabel } from '../lib/window-usage'

const selectClassName =
  'border-input bg-background text-foreground focus-visible:border-ring h-9 rounded-md border px-2.5 text-sm'

interface UsageWindowReportDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function UsageWindowReportDialog(props: UsageWindowReportDialogProps) {
  const { t } = useTranslation()
  const [windowKind, setWindowKind] = useState('5h')
  const [groupBy, setGroupBy] = useState('group')
  const [days, setDays] = useState(7)
  const { data, isLoading } = useQuery({
    queryKey: ['usage-window-report', windowKind, groupBy, days],
    queryFn: () => getUsageWindowReport({ window: windowKind, groupBy, days }),
    enabled: props.open,
    staleTime: 30 * 1000,
  })
  const rows = data?.rows ?? []

  let body: ReactNode
  if (isLoading) {
    body = (
      <div className='space-y-2'>
        <Skeleton className='h-5 w-full' />
        <Skeleton className='h-5 w-5/6' />
        <Skeleton className='h-5 w-4/6' />
      </div>
    )
  } else if (rows.length === 0) {
    body = (
      <p className='text-muted-foreground text-sm'>
        {t('No usage in this window.')}
      </p>
    )
  } else {
    body = (
      <div className='max-h-[55vh] overflow-auto rounded-md border'>
        <table className='w-full text-sm'>
          <thead className='bg-muted/50 text-muted-foreground sticky top-0 text-left text-xs uppercase'>
            <tr>
              <th className='px-3 py-2 font-medium'>{t('Window start')}</th>
              <th className='px-3 py-2 font-medium'>
                {groupBy === 'model' ? t('Model') : t('Group')}
              </th>
              <th className='px-3 py-2 font-medium'>{t('Cost')}</th>
              <th className='px-3 py-2 font-medium'>{t('Requests')}</th>
              <th className='px-3 py-2 font-medium'>{t('Tokens')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr className='border-t' key={`${row.window_start}-${row.key}`}>
                <td className='px-3 py-2 font-mono text-xs whitespace-nowrap'>
                  {formatTimestampToDate(row.window_start)}
                </td>
                <td className='px-3 py-2'>{row.key}</td>
                <td className='px-3 py-2 tabular-nums'>
                  {formatLogQuota(row.quota)}
                </td>
                <td className='px-3 py-2 tabular-nums'>{row.requests}</td>
                <td className='px-3 py-2 tabular-nums'>
                  {formatTokens(row.prompt_tokens + row.completion_tokens)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    )
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Window Report')}
      description={t('Usage and cost grouped by fixed quota windows.')}
      contentClassName='sm:max-w-3xl'
    >
      <div className='space-y-4 py-2'>
        <div className='flex flex-wrap items-center gap-2'>
          <select
            aria-label={t('Window')}
            className={selectClassName}
            onChange={(event) => setWindowKind(event.target.value)}
            value={windowKind}
          >
            <option value='5h'>{t('5-hour window')}</option>
            <option value='7d'>{t('7-day window')}</option>
          </select>
          <select
            aria-label={t('Group By')}
            className={selectClassName}
            onChange={(event) => setGroupBy(event.target.value)}
            value={groupBy}
          >
            <option value='group'>{t('Group')}</option>
            <option value='model'>{t('Model')}</option>
          </select>
          <select
            aria-label={t('Days')}
            className={selectClassName}
            onChange={(event) => setDays(Number(event.target.value))}
            value={days}
          >
            <option value={1}>{t('Last {{days}} days', { days: 1 })}</option>
            <option value={7}>{t('Last {{days}} days', { days: 7 })}</option>
            <option value={30}>{t('Last {{days}} days', { days: 30 })}</option>
          </select>
          <span className='text-muted-foreground text-xs'>
            {t('Total')}: {formatLogQuota(data?.total_quota ?? 0)} ·{' '}
            {t('Requests')}: {data?.total_requests ?? 0}
          </span>
        </div>

        {body}
        <p className='text-muted-foreground text-xs'>
          {windowKindLabel(windowKind)} · {t('Days')}: {days}
        </p>
      </div>
    </Dialog>
  )
}
