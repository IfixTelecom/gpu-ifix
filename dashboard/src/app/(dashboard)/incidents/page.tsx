"use client";

/**
 * Incident History — the audit-log view (OBS-03 "incident-history").
 *
 * Polls `fetchAudit` via `useQuery`; the `/admin/audit` handler returns rows
 * newest-first, rendered as-is by `AuditTable`. A limit/offset pager walks
 * the history (the page owns the offset query state).
 *
 * Loading → `skeleton`; fetch failure → the pt-BR error state with a
 * "Tentar novamente" button (UI-SPEC §Copywriting).
 */

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { AuditTable } from "@/components/audit-table";
import { StaleIndicator } from "@/components/stale-indicator";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { fetchAudit, GatewayError } from "@/lib/gateway";

const PAGE_SIZE = 50;

export default function IncidentsPage() {
  const [offset, setOffset] = useState(0);

  const { data, isLoading, isError, error, refetch, dataUpdatedAt } = useQuery({
    queryKey: ["audit", offset],
    queryFn: () => fetchAudit(PAGE_SIZE, offset),
  });

  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-[28px] font-semibold leading-[1.2]">
          Histórico de incidentes
        </h1>
        <StaleIndicator updatedAt={dataUpdatedAt} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-[20px] font-semibold">
            Eventos de mudança de estado
          </CardTitle>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <Skeleton className="h-[480px] w-full" />
          ) : isError ? (
            <div className="flex flex-col items-center gap-4 py-8 text-center">
              <p className="text-[14px] text-muted-foreground">
                {/* WR-06: show the specific proxy/gateway cause. */}
                {error instanceof GatewayError
                  ? error.message
                  : "Não foi possível carregar as métricas do gateway."}{" "}
                Verifique se o gateway está no ar e se a admin-key está válida,
                depois use Tentar novamente.
              </p>
              <Button size="sm" variant="outline" onClick={() => refetch()}>
                Tentar novamente
              </Button>
            </div>
          ) : (
            <AuditTable
              rows={data?.items ?? []}
              limit={data?.limit ?? PAGE_SIZE}
              offset={data?.offset ?? offset}
              onPrev={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
              onNext={() => setOffset((o) => o + PAGE_SIZE)}
            />
          )}
        </CardContent>
      </Card>
    </div>
  );
}
