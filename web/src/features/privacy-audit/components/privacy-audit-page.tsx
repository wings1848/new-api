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
import { useState } from "react";

import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { SectionPageLayout } from "@/components/layout";
import { Button } from "@/components/ui/button";

import { getPrivacyAuditEvents, getPrivacyAuditStats } from "../api";
import { modeLabelKey } from "../lib/format";
import { PrivacyAuditCleanupDialog } from "./cleanup-dialog";
import { PrivacyAuditDailyTrend } from "./daily-trend";
import { PrivacyAuditDistribution } from "./distribution-card";
import { PrivacyAuditEventsTable } from "./events-table";
import { PrivacyAuditStatsCards } from "./stats-cards";

const PAGE_SIZE = 20;
const DAY_OPTIONS = [7, 30, 90];
const MODE_OPTIONS = ["", "warn", "redact", "block"];

const selectClassName =
  "border-input bg-background text-foreground focus-visible:border-ring h-9 rounded-md border px-2.5 text-sm";

export function PrivacyAuditPage() {
  const { t } = useTranslation();
  const [days, setDays] = useState(7);
  const [mode, setMode] = useState("");
  const [type, setType] = useState("");
  const [page, setPage] = useState(1);

  const statsQuery = useQuery({
    queryKey: ["privacy-audit-stats", days],
    queryFn: () => getPrivacyAuditStats(days),
  });
  const eventsQuery = useQuery({
    queryKey: ["privacy-audit-events", days, mode, type, page],
    queryFn: () =>
      getPrivacyAuditEvents({
        page,
        page_size: PAGE_SIZE,
        days,
        mode,
        type,
      }),
  });
  const typeOptions = statsQuery.data?.by_type.map((item) => item.key) ?? [];
  const failed = statsQuery.isError || eventsQuery.isError;

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t("Privacy Audit")}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <PrivacyAuditCleanupDialog />
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-muted-foreground text-sm">
              {t("Dry-run privacy hits and masked request audit records.")}
            </p>
            <div className="flex items-center gap-2">
              <select
                aria-label={t("Last {{days}} days", { days })}
                className={selectClassName}
                onChange={(event) => {
                  setDays(Number(event.target.value));
                  setPage(1);
                }}
                value={days}
              >
                {DAY_OPTIONS.map((option) => (
                  <option key={option} value={option}>
                    {t("Last {{days}} days", { days: option })}
                  </option>
                ))}
              </select>
              <Button
                onClick={() => {
                  void statsQuery.refetch();
                  void eventsQuery.refetch();
                }}
                size="sm"
                variant="outline"
              >
                {t("Refresh")}
              </Button>
            </div>
          </div>

          {failed && (
            <p className="text-destructive text-sm">
              {t("Failed to load privacy audit data.")}
            </p>
          )}

          <PrivacyAuditStatsCards
            loading={statsQuery.isLoading}
            stats={statsQuery.data}
          />

          <div className="grid gap-4 lg:grid-cols-2">
            <PrivacyAuditDistribution
              counts={statsQuery.data?.by_type}
              title={t("By Rule Type")}
            />
            <PrivacyAuditDistribution
              counts={statsQuery.data?.by_mode}
              title={t("By Mode")}
            />
          </div>

          <PrivacyAuditDailyTrend stats={statsQuery.data} />

          <div className="flex flex-wrap items-center gap-2">
            <select
              aria-label={t("Mode")}
              className={selectClassName}
              onChange={(event) => {
                setMode(event.target.value);
                setPage(1);
              }}
              value={mode}
            >
              <option value="">{t("All")}</option>
              {MODE_OPTIONS.filter(Boolean).map((option) => (
                <option key={option} value={option}>
                  {t(modeLabelKey(option))}
                </option>
              ))}
            </select>
            <select
              aria-label={t("By Rule Type")}
              className={selectClassName}
              onChange={(event) => {
                setType(event.target.value);
                setPage(1);
              }}
              value={type}
            >
              <option value="">{t("All")}</option>
              {typeOptions.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
          </div>

          <PrivacyAuditEventsTable
            events={eventsQuery.data?.items}
            loading={eventsQuery.isLoading}
            onPageChange={setPage}
            page={eventsQuery.data?.page ?? page}
            pageSize={PAGE_SIZE}
            total={eventsQuery.data?.total}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  );
}
