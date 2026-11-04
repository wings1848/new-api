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
import { describe, expect, it } from "vitest";

import { dailyBarWidth, modeLabelKey, ruleSummary, topRule } from "../format";
import type { PrivacyAuditStats } from "../../types";

const stats: PrivacyAuditStats = {
  window_days: 7,
  total_events: 12,
  total_findings: 30,
  affected_users: 4,
  by_type: [
    { key: "api_key", count: 20 },
    { key: "email", count: 10 },
  ],
  by_action: [{ key: "redact", count: 25 }],
  by_rule: [{ key: "api-key", count: 20 }],
  by_mode: [{ key: "warn", count: 12 }],
  daily: [
    { date: "2026-11-04", events: 2, findings: 5 },
    { date: "2026-11-05", events: 10, findings: 25 },
  ],
};

describe("privacy audit helpers", () => {
  it("maps audit modes to i18n label keys", () => {
    expect(modeLabelKey("warn")).toBe("Warn");
    expect(modeLabelKey("redact")).toBe("Redact");
    expect(modeLabelKey("block")).toBe("Block");
    expect(modeLabelKey("other")).toBe("other");
  });

  it("aggregates finding rule counts", () => {
    expect(
      ruleSummary([
        { rule_id: "api-key", type: "api_key", action: "redact", count: 2 },
        { rule_id: "email", type: "email", action: "warn", count: 1 },
        { rule_id: "api-key", type: "api_key", action: "redact", count: 3 },
      ]),
    ).toEqual([
      { key: "api_key", count: 5 },
      { key: "email", count: 1 },
    ]);
  });

  it("finds the top rule of the window", () => {
    expect(topRule(stats)).toEqual({ key: "api_key", count: 20 });
    expect(topRule({ ...stats, by_type: [] })).toBeNull();
  });

  it("computes relative daily bar widths", () => {
    expect(dailyBarWidth(25, 25)).toBe(100);
    expect(dailyBarWidth(5, 25)).toBe(20);
    expect(dailyBarWidth(0, 0)).toBe(0);
    expect(dailyBarWidth(-5, 25)).toBe(0);
  });
});
