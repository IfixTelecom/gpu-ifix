"use client";

/**
 * Audit-log / incident-history table — a shadcn `table` (inside a
 * `scroll-area`) of `fetchAudit().rows`, newest-first, with a limit/offset
 * pager.
 *
 * Columns follow the ACTUAL `AuditRow` shape from 07-07's gateway.ts
 * (ts, event_kind, tenant_id, actor, detail) — the binding interface — not
 * the stale column list in the plan prose (route/status_code/latency_ms are
 * not in the `/admin/audit` response; T-07-31 keeps content out of the UI).
 *
 * UI-SPEC §Layout Constraints — 36px fixed rows. §Copywriting — the
 * "Nenhum evento registrado no período." empty state.
 */

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { AuditRow } from "@/lib/gateway";

export interface AuditTableProps {
  rows: AuditRow[];
  /** Current page size + offset, plus the server-reported total. */
  limit: number;
  offset: number;
  total: number;
  /** Pager callbacks — the page owns the limit/offset query state. */
  onPrev: () => void;
  onNext: () => void;
}

/** Render the `detail` JSON blob compactly (e.g. fsm from→to). */
function formatDetail(detail: AuditRow["detail"]): string {
  if (!detail) return "—";
  return Object.entries(detail)
    .map(([k, v]) => `${k}: ${String(v)}`)
    .join(" · ");
}

export function AuditTable({
  rows,
  limit,
  offset,
  total,
  onPrev,
  onNext,
}: AuditTableProps) {
  if (rows.length === 0) {
    return (
      <p className="py-8 text-center text-[14px] text-muted-foreground">
        Nenhum evento registrado no período.
      </p>
    );
  }

  const from = offset + 1;
  const to = offset + rows.length;
  const canPrev = offset > 0;
  const canNext = offset + limit < total;

  return (
    <div className="flex flex-col gap-4">
      <ScrollArea className="h-[480px] w-full">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="text-[12px] font-semibold">
                Horário
              </TableHead>
              <TableHead className="text-[12px] font-semibold">
                Evento
              </TableHead>
              <TableHead className="text-[12px] font-semibold">
                Tenant
              </TableHead>
              <TableHead className="text-[12px] font-semibold">Ator</TableHead>
              <TableHead className="text-[12px] font-semibold">
                Detalhe
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {/* `/admin/audit` returns rows newest-first — rendered as-is. */}
            {rows.map((row) => (
              <TableRow key={row.id} className="h-9">
                <TableCell className="text-[14px] tabular-nums">
                  {new Date(row.ts).toLocaleString("pt-BR")}
                </TableCell>
                <TableCell>
                  <Badge variant="outline" className="text-[12px] font-semibold">
                    {row.event_kind}
                  </Badge>
                </TableCell>
                <TableCell className="text-[14px]">
                  {row.tenant_id ?? "—"}
                </TableCell>
                <TableCell className="text-[14px] text-muted-foreground">
                  {row.actor ?? "—"}
                </TableCell>
                <TableCell className="text-[14px] text-muted-foreground">
                  {formatDetail(row.detail)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </ScrollArea>

      {/* limit/offset pager. */}
      <div className="flex items-center justify-between gap-4">
        <span className="text-[12px] font-semibold text-muted-foreground tabular-nums">
          {from}–{to} de {total}
        </span>
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={onPrev}
            disabled={!canPrev}
          >
            Anteriores
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={onNext}
            disabled={!canNext}
          >
            Próximos
          </Button>
        </div>
      </div>
    </div>
  );
}
