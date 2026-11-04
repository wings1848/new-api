import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import type { QuotaWindowMode } from '../lib/quota-window'

interface QuotaWindowLimitFieldProps {
  label: string
  mode: QuotaWindowMode
  amount: number
  currencyLabel: string
  onModeChange: (mode: QuotaWindowMode) => void
  onAmountChange: (amount: number) => void
}

/** One window limit editor: inherit group default, unlimited, or a custom amount. */
export function QuotaWindowLimitField(props: QuotaWindowLimitFieldProps) {
  const { t } = useTranslation()
  return (
    <div className='space-y-2'>
      <Label>{props.label}</Label>
      <div className='flex gap-2'>
        <Select
          items={[
            { value: 'inherit', label: t('Inherit group default') },
            { value: 'unlimited', label: t('Unlimited') },
            { value: 'custom', label: t('Custom') },
          ]}
          onValueChange={(value) =>
            value !== null && props.onModeChange(value as QuotaWindowMode)
          }
          value={props.mode}
        >
          <SelectTrigger className='w-[190px]'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              <SelectItem value='inherit'>{t('Inherit group default')}</SelectItem>
              <SelectItem value='unlimited'>{t('Unlimited')}</SelectItem>
              <SelectItem value='custom'>{t('Custom')}</SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
        {props.mode === 'custom' && (
          <Input
            className='flex-1'
            min={0}
            onChange={(event) =>
              props.onAmountChange(Number.parseFloat(event.target.value) || 0)
            }
            placeholder={t('Amount in {{currency}}', {
              currency: props.currencyLabel,
            })}
            step='0.01'
            type='number'
            value={String(props.amount)}
          />
        )}
      </div>
    </div>
  )
}
