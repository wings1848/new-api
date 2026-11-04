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
import type { PrivacyAuditCount } from "../types";

interface PrivacyAuditDistributionProps {
  title: string;
  counts?: PrivacyAuditCount[];
}

export function PrivacyAuditDistribution(props: PrivacyAuditDistributionProps) {
  const { t } = useTranslation();
  const counts = props.counts ?? [];
  const max = counts.reduce(
    (current, item) => Math.max(current, item.count),
    0,
  );
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm font-medium">{props.title}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {counts.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t("No data")}</p>
        ) : (
          counts.map((item) => (
            <div key={item.key} className="space-y-1">
              <div className="flex items-center justify-between gap-2 text-sm">
                <span className="truncate font-mono text-xs">{item.key}</span>
                <span className="text-muted-foreground tabular-nums">
                  {item.count}
                </span>
              </div>
              <div className="bg-muted h-1.5 overflow-hidden rounded-full">
                <div
                  className="bg-primary h-full rounded-full"
                  style={{ width: `${dailyBarWidth(item.count, max)}%` }}
                />
              </div>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}
