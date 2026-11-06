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
import type { ChannelQuotaWindowKind } from '../types'

/**
 * Upstream hosts whose quota endpoints the backend supports. The entry is only
 * offered for channels bound to one of them, because every other host is
 * rejected by GET /api/channel/:id/quota with `provider_unsupported`.
 */
const CHANNEL_QUOTA_SUPPORTED_HOSTS = new Set([
  'api.commandcode.ai',
  'opencode.ai',
])

export function isChannelQuotaSupported(baseUrl?: string | null): boolean {
  const trimmed = baseUrl?.trim()
  if (!trimmed) return false
  try {
    return CHANNEL_QUOTA_SUPPORTED_HOSTS.has(
      new URL(trimmed).hostname.toLowerCase()
    )
  } catch {
    return false
  }
}

/**
 * Window kinds are rendered from these keys; the English `name` in the API
 * payload is provider wording and is not shown.
 */
export const CHANNEL_QUOTA_WINDOW_LABEL_KEYS: Partial<
  Record<ChannelQuotaWindowKind, string>
> = {
  '5h': '5-Hour Window',
  week: 'Weekly Window',
  month: 'Monthly Window',
}

/**
 * Stable backend error codes of GET /api/channel/:id/quota mapped to
 * translated messages. Unknown codes fall back to the server `message`.
 */
export const CHANNEL_QUOTA_ERROR_KEYS: Record<string, string> = {
  provider_unsupported:
    'This channel does not support upstream quota monitoring',
  multi_key_unsupported:
    'Upstream quota monitoring is not supported for multi-key channels',
  channel_key_missing: 'The channel key is missing',
  upstream_unauthorized: 'The upstream rejected the channel key (HTTP 401)',
  upstream_forbidden: 'The upstream refused the quota request (HTTP 403)',
  no_subscription: 'This key has no active subscription',
  upstream_not_found: 'The upstream quota endpoint was not found (HTTP 404)',
  upstream_unavailable: 'The upstream quota service is unavailable',
  invalid_response: 'The upstream returned an unexpected quota response',
}
