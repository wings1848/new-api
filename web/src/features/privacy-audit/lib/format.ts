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
import dayjs from "dayjs";

import type {
  PrivacyAuditCount,
  PrivacyAuditFinding,
  PrivacyAuditStats,
} from "../types";

export function modeLabelKey(mode: string): string {
  switch (mode) {
    case "warn":
      return "Warn";
    case "redact":
      return "Redact";
    case "block":
      return "Block";
    default:
      return mode;
  }
}

export function ruleSummary(
  findings: PrivacyAuditFinding[],
): PrivacyAuditCount[] {
  const counts = new Map<string, number>();
  for (const finding of findings) {
    counts.set(finding.type, (counts.get(finding.type) ?? 0) + finding.count);
  }
  return [...counts.entries()]
    .map(([key, count]) => ({ key, count }))
    .sort((left, right) => right.count - left.count);
}

export function topRule(stats: PrivacyAuditStats): PrivacyAuditCount | null {
  if (!stats.by_type || stats.by_type.length === 0) {
    return null;
  }
  return stats.by_type[0];
}

export function dailyBarWidth(value: number, max: number): number {
  if (max <= 0 || value <= 0) {
    return 0;
  }
  return Math.min(100, Math.round((value * 100) / max));
}

export function formatAuditTime(timestamp: number): string {
  return dayjs.unix(timestamp).format("YYYY-MM-DD HH:mm:ss");
}
