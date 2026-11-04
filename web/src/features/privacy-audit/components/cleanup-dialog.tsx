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
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";

import { cleanupPrivacyAuditEvents } from "../api";

const CLEANUP_DAYS = 30;

export function PrivacyAuditCleanupDialog() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: () => cleanupPrivacyAuditEvents(CLEANUP_DAYS),
    onSuccess: (result) => {
      toast.success(
        t("Deleted {{events}} events and {{findings}} findings.", {
          events: result.deleted_events,
          findings: result.deleted_findings,
        }),
      );
      void queryClient.invalidateQueries({ queryKey: ["privacy-audit-stats"] });
      void queryClient.invalidateQueries({
        queryKey: ["privacy-audit-events"],
      });
    },
    onError: () => toast.error(t("Failed to load privacy audit data.")),
  });
  return (
    <AlertDialog>
      <AlertDialogTrigger render={<Button variant="outline" size="sm" />}>
        {t("Cleanup")}
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("Cleanup privacy audit records")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t(
              "Delete privacy audit records older than 30 days. This cannot be undone.",
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("Cancel")}</AlertDialogCancel>
          <AlertDialogAction
            disabled={mutation.isPending}
            onClick={(event) => {
              event.preventDefault();
              mutation.mutate();
            }}
          >
            {t("Cleanup")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
