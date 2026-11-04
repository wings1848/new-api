import { describe, expect, it } from 'vitest'

import {
  buildQuotaWindowUpdatePayload,
  quotaWindowFormStateFromOverview,
  quotaWindowModeFromOverride,
} from '../../lib/quota-window'
import type { QuotaWindowOverview } from '../../types'

const overview: QuotaWindowOverview = {
  group: 'default',
  windows: [
    { kind: '5h', limit: 100, used: 20, remaining: 80, reset_at: 1793808000, source: 'user' },
    { kind: '7d', limit: 1000, used: 200, remaining: 800, reset_at: 1793548800, source: 'group' },
  ],
  user_overrides: { window_5h_quota: -1, window_7d_quota: 500000000 },
  server_time: 1793800000,
}

describe('quota window limit form helpers', () => {
  it('maps a raw override value to a mode', () => {
    expect(quotaWindowModeFromOverride(-1)).toBe('inherit')
    expect(quotaWindowModeFromOverride(0)).toBe('unlimited')
    expect(quotaWindowModeFromOverride(500000)).toBe('custom')
  })

  it('builds form state from the admin overview', () => {
    const state = quotaWindowFormStateFromOverview(overview)
    expect(state.fiveHourMode).toBe('inherit')
    expect(state.sevenDayMode).toBe('custom')
    expect(state.sevenDayAmount).toBe(1000)
  })

  it('builds the update payload from the form state', () => {
    const state = quotaWindowFormStateFromOverview(overview)
    expect(buildQuotaWindowUpdatePayload(state)).toEqual({
      window_5h_quota: -1,
      window_7d_quota: 500000000,
    })
    expect(
      buildQuotaWindowUpdatePayload({
        fiveHourMode: 'custom',
        fiveHourAmount: 2,
        sevenDayMode: 'unlimited',
        sevenDayAmount: 0,
      })
    ).toEqual({ window_5h_quota: 1000000, window_7d_quota: 0 })
  })
})
