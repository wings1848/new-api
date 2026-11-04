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
export interface ApiResponse<T = unknown> {
  success: boolean;
  message?: string;
  data?: T;
}

export interface PrivacyAuditCount {
  key: string;
  count: number;
}

export interface PrivacyAuditDaily {
  date: string;
  events: number;
  findings: number;
}

export interface PrivacyAuditStats {
  window_days: number;
  total_events: number;
  total_findings: number;
  affected_users: number;
  by_type: PrivacyAuditCount[];
  by_action: PrivacyAuditCount[];
  by_rule: PrivacyAuditCount[];
  by_mode: PrivacyAuditCount[];
  daily: PrivacyAuditDaily[];
}

export interface PrivacyAuditFinding {
  rule_id: string;
  type: string;
  action: string;
  count: number;
}

export interface PrivacyAuditEvent {
  id: number;
  user_id: number;
  username?: string;
  request_id?: string;
  path: string;
  mode: string;
  finding_count: number;
  created_at: number;
  findings: PrivacyAuditFinding[];
}

export interface PrivacyAuditEventsPage {
  items: PrivacyAuditEvent[];
  total: number;
  page: number;
  page_size: number;
}

export interface PrivacyAuditCleanupResult {
  deleted_events: number;
  deleted_findings: number;
}

export interface PrivacyAuditEventsQuery {
  page: number;
  page_size: number;
  days: number;
  mode?: string;
  type?: string;
}
