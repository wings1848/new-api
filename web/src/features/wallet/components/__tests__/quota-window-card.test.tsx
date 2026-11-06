import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DEFAULT_CURRENCY_CONFIG, useSystemConfigStore } from '@/stores/system-config-store'
import { api } from '@/lib/api'

import type { QuotaWindowItem, QuotaWindowOverview } from '@/features/users/types'
import type { UsageWindowSummary } from '@/features/usage-logs/types'

import { QuotaWindowCard } from '../quota-window-card'

const SERVER_TIME = 1000
const RESET_IN = 3 * 3600 + 12 * 60

function windowItem(overrides: Partial<QuotaWindowItem>): QuotaWindowItem {
  return {
    kind: '5h',
    limit: 500000,
    used: 400000,
    remaining: 100000,
    reset_at: SERVER_TIME + RESET_IN,
    source: 'group',
    ...overrides,
  }
}

function overview(windows: QuotaWindowItem[]): QuotaWindowOverview {
  return {
    group: 'default',
    windows,
    user_overrides: { window_5h_quota: -1, window_7d_quota: -1 },
    server_time: SERVER_TIME,
  }
}

function summary(windows: UsageWindowSummary['windows']): UsageWindowSummary {
  return { windows, server_time: SERVER_TIME }
}

function mockWindowData(
  overviewData: QuotaWindowOverview,
  summaryData: UsageWindowSummary
) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/quota-window/self') {
      return { data: { success: true, data: overviewData } }
    }
    if (url === '/api/usage-window/summary') {
      return { data: { success: true, data: summaryData } }
    }
    return { data: { success: true } }
  })
}

function renderCard() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <QuotaWindowCard />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

afterEach(() => {
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

describe('quota window card', () => {
  it('shows used over limit, the usage percentage and the relative reset countdown', async () => {
    mockWindowData(
      overview([
        windowItem({ kind: '5h' }),
        windowItem({
          kind: '7d',
          limit: 0,
          used: 250000,
          remaining: 0,
        }),
      ]),
      summary([])
    )

    renderCard()

    expect(await screen.findByText('5-hour window')).toBeInTheDocument()
    expect(screen.getByText('80%')).toHaveClass('text-warning')
    expect(screen.getAllByRole('progressbar')).toHaveLength(1)
    expect(screen.getByText('Unlimited')).toBeInTheDocument()
    expect(screen.getAllByText(/Reset in 3h 12m/)).toHaveLength(2)
  })

  it('flags a window that reached its limit', async () => {
    mockWindowData(
      overview([
        windowItem({ kind: '5h', used: 500000, remaining: 0 }),
      ]),
      summary([])
    )

    renderCard()

    expect(await screen.findByText('Limit reached')).toBeVisible()
    expect(screen.getByText('100%')).toHaveClass('text-destructive')
  })

  it('keeps the empty state when no window limit is configured', async () => {
    mockWindowData(
      overview([windowItem({ kind: '5h', limit: 0, used: 0, remaining: 0 })]),
      summary([])
    )

    renderCard()

    expect(
      await screen.findByText('No quota window limit configured.')
    ).toBeVisible()
  })

  it('shows the exhaustion estimate only while the window still has headroom', async () => {
    mockWindowData(
      overview([windowItem({ kind: '5h' })]),
      summary([
        {
          kind: '5h',
          limit: 500000,
          used: 400000,
          remaining: 100000,
          reset_at: SERVER_TIME + RESET_IN,
          source: 'group',
          estimated_exhaust_seconds: 2 * 3600,
        },
      ])
    )

    renderCard()

    expect(
      await screen.findByText('Estimated limit in 2h')
    ).toBeInTheDocument()
  })
})
