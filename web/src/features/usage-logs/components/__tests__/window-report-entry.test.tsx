import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { WindowReportButton } from '../window-report-button'

const REPORT = {
  window: '5h',
  group_by: 'group',
  days: 7,
  total_quota: 500000,
  total_requests: 4,
  generated_at: 1000,
  rows: [
    {
      key: 'default',
      window_start: 900,
      window_end: 1000,
      quota: 500000,
      requests: 4,
      prompt_tokens: 30,
      completion_tokens: 10,
    },
  ],
}

function mockReport() {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.startsWith('/api/usage-window/report')) {
      return { data: { success: true, data: REPORT } }
    }
    return { data: { success: true } }
  })
}

function setRole(role: number) {
  useAuthStore.setState((state) => ({
    auth: {
      ...state.auth,
      user: { id: 7, username: 'alice', role },
    },
  }))
}

function renderEntry() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <WindowReportButton />
    </QueryClientProvider>
  )
}

afterEach(() => {
  useAuthStore.getState().auth.reset()
})

describe('window report header entry', () => {
  it('lets an administrator open the window report', async () => {
    mockReport()
    setRole(ROLE.ADMIN)
    const user = userEvent.setup()

    renderEntry()

    await user.click(screen.getByRole('button', { name: 'Window Report' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Window Report')).toBeInTheDocument()
    expect(await within(dialog).findByText('default')).toBeInTheDocument()
  })

  it('does not render the entry for a regular user', () => {
    setRole(ROLE.USER)

    renderEntry()

    expect(
      screen.queryByRole('button', { name: 'Window Report' })
    ).not.toBeInTheDocument()
  })
})
