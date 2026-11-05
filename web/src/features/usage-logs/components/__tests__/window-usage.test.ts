import { describe, expect, it } from 'vitest'

import { formatDuration } from '@/lib/format'

import { exhaustEtaLabel, windowKindLabel } from '../../lib/window-usage'

describe('window usage helpers', () => {
  it('formats durations for the exhaustion estimate', () => {
    expect(formatDuration(0)).toBe('-')
    expect(formatDuration(30)).toBe('30s')
    expect(formatDuration(90)).toBe('1m')
    expect(formatDuration(3600)).toBe('1h')
    expect(formatDuration(5400)).toBe('1h 30m')
    expect(formatDuration(2 * 86400 + 5 * 3600)).toBe('2d')
  })

  it('maps window kinds to labels', () => {
    expect(windowKindLabel('5h')).toBe('5-hour window')
    expect(windowKindLabel('7d')).toBe('7-day window')
  })

  it('hides the ETA when there is no estimate', () => {
    expect(exhaustEtaLabel(0)).toBeNull()
    expect(exhaustEtaLabel(-5)).toBeNull()
    expect(exhaustEtaLabel(7200)).toBe('2h')
  })
})
