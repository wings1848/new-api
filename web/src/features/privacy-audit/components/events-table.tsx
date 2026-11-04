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
import type { ReactNode } from "react";

import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";

import { formatAuditTime, modeLabelKey, ruleSummary } from "../lib/format";
import type { PrivacyAuditEvent } from "../types";

type BadgeVariant = "destructive" | "default" | "secondary";

function modeBadgeVariant(mode: string): BadgeVariant {
  if (mode === "block") {
    return "destructive";
  }
  if (mode === "redact") {
    return "default";
  }
  return "secondary";
}

interface PrivacyAuditEventsTableProps {
  events?: PrivacyAuditEvent[];
  loading?: boolean;
  total?: number;
  page?: number;
  pageSize?: number;
  onPageChange?: (page: number) => void;
}

export function PrivacyAuditEventsTable(props: PrivacyAuditEventsTableProps) {
  const { t } = useTranslation();
  const events = props.events ?? [];
  const total = props.total ?? 0;
  const page = props.page ?? 1;
  const pageSize = props.pageSize ?? 20;
  const pages = Math.max(1, Math.ceil(total / pageSize));

  let body: ReactNode;
  if (props.loading) {
    body = (
      <div className="space-y-2 p-4">
        <Skeleton className="h-5 w-full" />
        <Skeleton className="h-5 w-5/6" />
        <Skeleton className="h-5 w-4/6" />
      </div>
    );
  } else if (events.length === 0) {
    body = (
      <p className="text-muted-foreground p-4 text-sm">
        {t("No privacy audit events yet.")}
      </p>
    );
  } else {
    body = (
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-muted-foreground border-b text-left text-xs uppercase">
            <tr>
              <th className="px-4 py-2 font-medium">{t("Time")}</th>
              <th className="px-4 py-2 font-medium">{t("User")}</th>
              <th className="px-4 py-2 font-medium">{t("Mode")}</th>
              <th className="px-4 py-2 font-medium">{t("Path")}</th>
              <th className="px-4 py-2 font-medium">{t("Findings")}</th>
              <th className="px-4 py-2 font-medium">{t("Rules")}</th>
            </tr>
          </thead>
          <tbody>
            {events.map((event) => (
              <tr key={event.id} className="border-b last:border-0">
                <td className="px-4 py-2 font-mono text-xs whitespace-nowrap">
                  {formatAuditTime(event.created_at)}
                </td>
                <td className="px-4 py-2 whitespace-nowrap">
                  {event.username || `#${event.user_id}`}
                </td>
                <td className="px-4 py-2">
                  <Badge variant={modeBadgeVariant(event.mode)}>
                    {t(modeLabelKey(event.mode))}
                  </Badge>
                </td>
                <td className="max-w-[280px] truncate px-4 py-2 font-mono text-xs">
                  {event.path}
                </td>
                <td className="px-4 py-2 tabular-nums">
                  {event.finding_count}
                </td>
                <td className="px-4 py-2">
                  <div className="flex flex-wrap gap-1">
                    {ruleSummary(event.findings).map((rule) => (
                      <Badge key={rule.key} variant="outline">
                        {rule.key} ×{rule.count}
                      </Badge>
                    ))}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  }

  return (
    <div className="rounded-lg border">
      <div className="flex items-center justify-between border-b px-4 py-3">
        <h3 className="text-sm font-medium">{t("Recent Events")}</h3>
        <span className="text-muted-foreground text-xs">
          {t("Count")}: {total}
        </span>
      </div>
      {body}
      <div className="flex items-center justify-between border-t px-4 py-2 text-xs">
        <span className="text-muted-foreground">
          {t("Page")} {page} / {pages}
        </span>
        <div className="flex gap-2">
          <button
            className="hover:bg-muted rounded-md border px-2 py-1 disabled:opacity-50"
            disabled={page <= 1}
            onClick={() => props.onPageChange?.(page - 1)}
            type="button"
          >
            {t("Previous")}
          </button>
          <button
            className="hover:bg-muted rounded-md border px-2 py-1 disabled:opacity-50"
            disabled={page >= pages}
            onClick={() => props.onPageChange?.(page + 1)}
            type="button"
          >
            {t("Next")}
          </button>
        </div>
      </div>
    </div>
  );
}
