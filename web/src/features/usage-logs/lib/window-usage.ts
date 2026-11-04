import { formatDuration } from '@/lib/format'

export function windowKindLabel(kind: string): string {
  return kind === '5h' ? '5-hour window' : '7-day window'
}

export function exhaustEtaLabel(seconds: number): string | null {
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return null
  }
  return formatDuration(seconds)
}
