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
import { api } from "@/lib/api";
import { requireServerSuccess } from "@/lib/server-error-message";

import type {
  ApiResponse,
  PrivacyAuditCleanupResult,
  PrivacyAuditEventsPage,
  PrivacyAuditEventsQuery,
  PrivacyAuditStats,
} from "./types";

export async function getPrivacyAuditStats(
  days: number,
): Promise<PrivacyAuditStats> {
  const response = await api.get<ApiResponse<PrivacyAuditStats>>(
    `/api/privacy-audit/stats?days=${days}`,
  );
  requireServerSuccess(response.data);
  return response.data.data as PrivacyAuditStats;
}

export async function getPrivacyAuditEvents(
  query: PrivacyAuditEventsQuery,
): Promise<PrivacyAuditEventsPage> {
  const params = new URLSearchParams({
    page: String(query.page),
    page_size: String(query.page_size),
    days: String(query.days),
  });
  if (query.mode) {
    params.set("mode", query.mode);
  }
  if (query.type) {
    params.set("type", query.type);
  }
  const response = await api.get<ApiResponse<PrivacyAuditEventsPage>>(
    `/api/privacy-audit/events?${params.toString()}`,
  );
  requireServerSuccess(response.data);
  return response.data.data as PrivacyAuditEventsPage;
}

export async function cleanupPrivacyAuditEvents(
  olderThanDays: number,
): Promise<PrivacyAuditCleanupResult> {
  const response = await api.post<ApiResponse<PrivacyAuditCleanupResult>>(
    "/api/privacy-audit/cleanup",
    { older_than_days: olderThanDays },
  );
  requireServerSuccess(response.data);
  return response.data.data as PrivacyAuditCleanupResult;
}
