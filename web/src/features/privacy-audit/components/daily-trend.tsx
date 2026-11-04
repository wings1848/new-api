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
import { useTranslation } from "react-i18next";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

import { dailyBarWidth } from "../lib/format";
import type { PrivacyAuditStats } from "../types";

interface PrivacyAuditDailyTrendProps {
  stats?: PrivacyAuditStats;
}

export function PrivacyAuditDailyTrend(props: PrivacyAuditDailyTrendProps) {
  const { t } = useTranslation();
  const daily = props.stats?.daily ?? [];
  const max = daily.reduce(
    (current, item) => Math.max(current, item.findings),
    0,
  );
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm font-medium">
          {t("Daily Trend")}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {daily.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t("No data")}</p>
        ) : (
          <div className="max-h-64 space-y-1.5 overflow-y-auto">
            {daily.map((item) => (
              <div key={item.date} className="flex items-center gap-3 text-xs">
                <span className="text-muted-foreground w-24 shrink-0 font-mono">
                  {item.date}
                </span>
                <div className="bg-muted h-2 flex-1 overflow-hidden rounded-full">
                  <div
                    className="bg-primary h-full rounded-full"
                    style={{ width: `${dailyBarWidth(item.findings, max)}%` }}
                  />
                </div>
                <span className="w-24 shrink-0 text-right tabular-nums">
                  {t("Events")} {item.events} · {t("Findings")} {item.findings}
                </span>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
