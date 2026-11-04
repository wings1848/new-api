import { parseQuotaFromDollars, quotaUnitsToDollars } from '@/lib/format'

import type { QuotaWindowOverview } from '../types'

// Helpers for the 5h/7d quota-window admin form. The backend stores limits in
// quota units; -1 means "inherit the next scope", 0 means explicit no-limit.

export type QuotaWindowMode = 'inherit' | 'unlimited' | 'custom'

export interface QuotaWindowLimitState {
  fiveHourMode: QuotaWindowMode
  fiveHourAmount: number
  sevenDayMode: QuotaWindowMode
  sevenDayAmount: number
}

export interface QuotaWindowUpdatePayload {
  window_5h_quota?: number
  window_7d_quota?: number
}

export function quotaWindowModeFromOverride(value: number): QuotaWindowMode {
  if (value < 0) {
    return 'inherit'
  }
  if (value === 0) {
    return 'unlimited'
  }
  return 'custom'
}

function quotaWindowAmountFromOverride(value: number): number {
  return value > 0 ? quotaUnitsToDollars(value) : 0
}

export function quotaWindowOverrideFromState(
  mode: QuotaWindowMode,
  amount: number
): number {
  switch (mode) {
    case 'inherit':
      return -1
    case 'unlimited':
      return 0
    case 'custom':
      return parseQuotaFromDollars(amount)
  }
}

export function quotaWindowFormStateFromOverview(
  overview: QuotaWindowOverview
): QuotaWindowLimitState {
  const fiveHour = overview.user_overrides?.window_5h_quota ?? -1
  const sevenDay = overview.user_overrides?.window_7d_quota ?? -1
  return {
    fiveHourMode: quotaWindowModeFromOverride(fiveHour),
    fiveHourAmount: quotaWindowAmountFromOverride(fiveHour),
    sevenDayMode: quotaWindowModeFromOverride(sevenDay),
    sevenDayAmount: quotaWindowAmountFromOverride(sevenDay),
  }
}

export function buildQuotaWindowUpdatePayload(
  state: QuotaWindowLimitState
): QuotaWindowUpdatePayload {
  return {
    window_5h_quota: quotaWindowOverrideFromState(
      state.fiveHourMode,
      state.fiveHourAmount
    ),
    window_7d_quota: quotaWindowOverrideFromState(
      state.sevenDayMode,
      state.sevenDayAmount
    ),
  }
}

export function quotaWindowLimitsChanged(
  state: QuotaWindowLimitState,
  overview: QuotaWindowOverview | null
): boolean {
  if (!overview) {
    return false
  }
  const current = quotaWindowFormStateFromOverview(overview)
  return (
    current.fiveHourMode !== state.fiveHourMode ||
    current.sevenDayMode !== state.sevenDayMode ||
    (state.fiveHourMode === 'custom' &&
      quotaWindowOverrideFromState(
        state.fiveHourMode,
        state.fiveHourAmount
      ) !==
        quotaWindowOverrideFromState(
          current.fiveHourMode,
          current.fiveHourAmount
        )) ||
    (state.sevenDayMode === 'custom' &&
      quotaWindowOverrideFromState(
        state.sevenDayMode,
        state.sevenDayAmount
      ) !==
        quotaWindowOverrideFromState(
          current.sevenDayMode,
          current.sevenDayAmount
        ))
  )
}
