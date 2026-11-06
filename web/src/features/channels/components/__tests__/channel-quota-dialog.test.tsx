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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  type Channel,
  type ChannelQuotaResponse,
  channelSchema,
} from '../../types'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { ChannelQuotaDialog } from '../dialogs/channel-quota-dialog'

const NOW = Date.now()
const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

const commandCodeChannel = channelSchema.parse({
  id: 2,
  type: 1,
  key: 'test-key',
  status: 1,
  name: 'commandcode',
  created_time: 0,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
  base_url: 'https://api.commandcode.ai/provider',
})

function commandCodeQuota(): ChannelQuotaResponse {
  return {
    success: true,
    data: {
      provider: 'commandcode',
      providerName: 'Command Code',
      channelId: 2,
      channelName: 'commandcode',
      plan: 'goat',
      windows: [
        {
          kind: '5h',
          name: '5-Hour',
          percent: 10.65,
          used: 1.49,
          cap: 14,
          resetsAt: NOW + 3 * HOUR + 12 * MINUTE,
        },
        {
          kind: 'week',
          name: 'Weekly',
          percent: 37.15,
          used: 13,
          cap: 35,
          resetsAt: NOW + 4 * DAY,
        },
        {
          kind: 'month',
          name: 'Monthly',
          percent: 60.35,
          used: 42.18,
          cap: 70,
          resetsAt: NOW + 5 * DAY,
        },
      ],
      extras: {
        monthlyCredits: 27.72,
        monthlySpent: 42.18,
        billingPeriodEnd: '2026-11-11T13:36:29.000Z',
      },
      warnings: ['Weekly window status: throttled'],
      fetchedAt: NOW,
    },
  }
}

function openCodeQuota(): ChannelQuotaResponse {
  return {
    success: true,
    data: {
      provider: 'opencode-go',
      providerName: 'OpenCode Go',
      channelId: 3,
      channelName: 'opencodego',
      windows: [
        { kind: '5h', name: 'Rolling', percent: 0, resetsAt: NOW + HOUR },
        {
          kind: 'week',
          name: 'Weekly',
          percent: 12.5,
          resetsAt: NOW + 2 * DAY,
        },
        {
          kind: 'month',
          name: 'Monthly',
          percent: 25.5,
          resetsAt: NOW + 20 * DAY,
        },
      ],
      fetchedAt: NOW,
    },
  }
}

function mockQuotaResponse(payload: ChannelQuotaResponse) {
  return vi.spyOn(api, 'get').mockResolvedValue({ data: payload })
}

function ChannelQuotaHarness(props: { open: boolean; channel: Channel }) {
  const { setCurrentRow } = useChannels()
  useEffect(() => {
    setCurrentRow(props.channel)
  }, [props.channel, setCurrentRow])
  return <ChannelQuotaDialog open={props.open} onOpenChange={() => undefined} />
}

function renderChannelQuotaDialog(channel: Channel = commandCodeChannel) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const ui = (open: boolean) => (
    <QueryClientProvider client={queryClient}>
      <ChannelsProvider>
        <ChannelQuotaHarness open={open} channel={channel} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
  const view = render(ui(true))
  return {
    ...view,
    rerenderOpen: (open: boolean) => view.rerender(ui(open)),
  }
}

describe('channel quota dialog', () => {
  test('renders the three windows with percentages, credits and reset details', async () => {
    mockQuotaResponse(commandCodeQuota())
    renderChannelQuotaDialog()

    expect(
      await screen.findByRole('heading', { name: 'Upstream Quota' })
    ).toBeInTheDocument()
    expect(screen.getByText('Command Code')).toBeInTheDocument()
    expect(screen.getByText('commandcode')).toBeInTheDocument()
    expect(screen.getByText('goat')).toBeInTheDocument()

    expect(screen.getByText('5-Hour Window')).toBeInTheDocument()
    expect(screen.getByText('10.65%')).toBeInTheDocument()
    expect(screen.getByText('1.49 / 14 credits')).toBeInTheDocument()
    expect(screen.getByText(/Resets in: 3h 12m/)).toBeInTheDocument()

    expect(screen.getByText('Weekly Window')).toBeInTheDocument()
    expect(screen.getByText('37.15%')).toBeInTheDocument()
    expect(screen.getByText('13 / 35 credits')).toBeInTheDocument()

    expect(screen.getByText('Monthly Window')).toBeInTheDocument()
    expect(screen.getByText('60.35%')).toBeInTheDocument()
    expect(screen.getByText('42.18 / 70 credits')).toBeInTheDocument()

    expect(screen.getByText('Upstream warnings')).toBeInTheDocument()
    expect(
      screen.getByText('Weekly window status: throttled')
    ).toBeInTheDocument()
    expect(screen.getByText('Credit remaining')).toBeInTheDocument()
    expect(screen.getByText('27.72')).toBeInTheDocument()
    expect(screen.getByText('Monthly spent')).toBeInTheDocument()
    expect(screen.getByText('Billing period ends')).toBeInTheDocument()
    expect(
      screen.getByText(/^2026-11-1[12] \d{2}:\d{2}:\d{2}$/)
    ).toBeInTheDocument()
  })

  test('uses warning and destructive colors when a window is nearly or fully used', async () => {
    mockQuotaResponse({
      success: true,
      data: {
        provider: 'commandcode',
        providerName: 'Command Code',
        channelId: 2,
        channelName: 'commandcode',
        windows: [
          { kind: '5h', name: '5-Hour', percent: 82.4 },
          { kind: 'week', name: 'Weekly', percent: 100 },
          { kind: 'month', name: 'Monthly', percent: 40 },
        ],
        fetchedAt: NOW,
      },
    })
    renderChannelQuotaDialog()

    expect(await screen.findByText('82.4%')).toHaveClass('text-warning')
    expect(screen.getByText('100%')).toHaveClass('text-destructive')
    expect(screen.getByText('40%')).toHaveClass('text-muted-foreground')
  })

  test('renders percent-only windows without credit amounts', async () => {
    mockQuotaResponse(openCodeQuota())
    renderChannelQuotaDialog()

    expect(await screen.findByText('OpenCode Go')).toBeInTheDocument()
    expect(screen.getByText('12.5%')).toBeInTheDocument()
    expect(screen.getByText('25.5%')).toBeInTheDocument()
    expect(screen.queryByText(/credits/)).not.toBeInTheDocument()
    expect(screen.queryByText('Credit remaining')).not.toBeInTheDocument()
  })

  test('maps an upstream error code to a readable message instead of the raw text', async () => {
    mockQuotaResponse({
      success: false,
      code: 'upstream_unauthorized',
      message: 'Command Code returned HTTP 401 for the quota request',
    })
    renderChannelQuotaDialog()

    expect(
      await screen.findByText(
        'The upstream rejected the channel key (HTTP 401)'
      )
    ).toBeInTheDocument()
    expect(
      screen.queryByText('Command Code returned HTTP 401 for the quota request')
    ).not.toBeInTheDocument()
  })

  test('explains that unsupported providers cannot be monitored', async () => {
    mockQuotaResponse({
      success: false,
      code: 'provider_unsupported',
      message: 'channel provider is not supported for quota monitoring',
    })
    renderChannelQuotaDialog()

    expect(
      await screen.findByText(
        'This channel does not support upstream quota monitoring'
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Only Command Code and OpenCode Go channels are supported.'
      )
    ).toBeInTheDocument()
  })

  test('falls back to the server message for an unknown error code', async () => {
    mockQuotaResponse({
      success: false,
      code: 'unexpected_code',
      message: 'The upstream quota service exploded',
    })
    renderChannelQuotaDialog()

    expect(
      await screen.findByText('The upstream quota service exploded')
    ).toBeInTheDocument()
  })

  test('refresh fetches the quota again', async () => {
    const get = mockQuotaResponse(commandCodeQuota())
    const user = userEvent.setup()
    renderChannelQuotaDialog()

    expect(await screen.findByText('10.65%')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    expect(get).toHaveBeenLastCalledWith(
      '/api/channel/2/quota',
      expect.objectContaining({ disableDuplicate: true })
    )
  })

  test('shows a skeleton until the first quota response arrives', async () => {
    vi.spyOn(api, 'get').mockReturnValue(new Promise<never>(() => {}))
    renderChannelQuotaDialog()

    await waitFor(() =>
      expect(
        document.querySelector('[data-slot="skeleton"]')
      ).toBeInTheDocument()
    )
    expect(screen.queryByText('5-Hour Window')).not.toBeInTheDocument()
  })

  test('aborts the in-flight request when the dialog closes', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockReturnValue(new Promise<never>(() => {}))
    const view = renderChannelQuotaDialog()

    await waitFor(() => expect(get).toHaveBeenCalledTimes(1))
    const signal = get.mock.calls[0]?.[1]?.signal
    expect(signal).toBeInstanceOf(AbortSignal)

    view.rerenderOpen(false)
    expect(signal?.aborted).toBe(true)
  })
})
