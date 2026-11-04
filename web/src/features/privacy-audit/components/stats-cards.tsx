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

import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

import { topRule } from "../lib/format";
import type { PrivacyAuditStats } from "../types";

interface PrivacyAuditStatsCardsProps {
  stats?: PrivacyAuditStats;
  loading?: boolean;
}

export function PrivacyAuditStatsCards(props: PrivacyAuditStatsCardsProps) {
  const { t } = useTranslation();
  const top = props.stats ? topRule(props.stats) : null;
  const items = [
    { label: t("Events"), value: props.stats?.total_events ?? 0 },
    { label: t("Findings"), value: props.stats?.total_findings ?? 0 },
    { label: t("Affected Users"), value: props.stats?.affected_users ?? 0 },
    { label: t("Top Rule"), value: top ? `${top.key} (${top.count})` : "—" },
  ];
  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {items.map((item) => (
        <Card key={item.label}>
          <CardContent className="p-4">
            <div className="text-muted-foreground text-xs font-medium tracking-wider uppercase">
              {item.label}
            </div>
            {props.loading ? (
              <Skeleton className="mt-2 h-7 w-24" />
            ) : (
              <div className="mt-1.5 font-mono text-2xl font-bold tracking-tight tabular-nums">
                {item.value}
              </div>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
