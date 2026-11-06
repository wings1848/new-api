import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { QuotaWindowUsage } from '../quota-window-usage'

const SERVER_TIME = 1000
const RESET_IN = 3 * 3600 + 12 * 60
const RESET_AT = SERVER_TIME + RESET_IN

describe('quota window usage meter', () => {
  it('shows used over limit, its percentage and the relative reset countdown', () => {
    render(
      <QuotaWindowUsage
        limit={500000}
        resetAt={RESET_AT}
        serverTime={SERVER_TIME}
        used={400000}
      />
    )

    expect(screen.getByText('80%')).toHaveClass('text-warning')
    const progress = screen.getByRole('progressbar')
    expect(progress).toHaveAttribute('aria-valuenow', '80')
    expect(progress.getAttribute('aria-label')).toContain('80%')
    expect(screen.getByText(/Reset in 3h 12m/)).toBeInTheDocument()
    expect(screen.getByText(/Reset at:/)).toBeInTheDocument()
  })

  it('marks a window at its limit as destructive', () => {
    render(
      <QuotaWindowUsage
        limit={500000}
        resetAt={RESET_AT}
        serverTime={SERVER_TIME}
        used={500000}
      />
    )

    expect(screen.getByText('100%')).toHaveClass('text-destructive')
  })

  it('shows unlimited windows without a progress bar', () => {
    render(
      <QuotaWindowUsage
        limit={0}
        resetAt={RESET_AT}
        serverTime={SERVER_TIME}
        used={250000}
      />
    )

    expect(screen.getByText('Unlimited')).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
    expect(screen.queryByText(/%$/)).not.toBeInTheDocument()
  })

  it('renders a single inline usage line for compact contexts', () => {
    render(
      <QuotaWindowUsage
        limit={500000}
        resetAt={RESET_AT}
        serverTime={SERVER_TIME}
        used={100000}
        variant='inline'
      />
    )

    expect(screen.getByText(/Used .* of .* \(20%\)/)).toHaveTextContent(
      'Reset in 3h 12m'
    )
  })

  it('omits the countdown when the reset moment has already passed', () => {
    render(
      <QuotaWindowUsage
        limit={500000}
        resetAt={SERVER_TIME - 60}
        serverTime={SERVER_TIME}
        used={100000}
        variant='inline'
      />
    )

    expect(screen.queryByText(/Reset in /)).not.toBeInTheDocument()
  })
})
