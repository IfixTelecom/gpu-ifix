"use client";

/**
 * Sticky critical/warning event banner — the UI-SPEC's primary focal point.
 *
 * Reads the `fetchMetrics` query and, based on `fsm_state`:
 *   - FAILED_OVER / EMERGENCY_ACTIVE → sticky-top RED (`--destructive`) banner
 *   - DEGRADED / EMERGENCY_PROVISIONING / RECOVERING / COOLDOWN
 *       → AMBER (`--status-warning`) banner
 *   - anything else → renders nothing
 *
 * "Reconhecer incidente" silences the banner LOCALLY for 5 min (mirroring the
 * alerting dedup TTL) — it is local component state only, NO gateway write
 * (threat T-07-30 "accept": the dashboard is read-only).
 *
 * Layout Constraints: the banner box is pinned to a 44px min-height; internal
 * padding uses the `sm`/8px spacing token.
 */

import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { useEffect, useState } from "react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { fetchMetrics } from "@/lib/gateway";
import { isCriticalFsmState, isWarningFsmState } from "@/lib/fsm";
import { cn } from "@/lib/utils";

/** Local acknowledge TTL — mirrors the alerting dedup window (5 min). */
const ACK_TTL_MS = 5 * 60 * 1000;

export function CriticalBanner() {
  const { data } = useQuery({
    queryKey: ["metrics"],
    queryFn: () => fetchMetrics(),
  });

  // Local-only acknowledge state — no gateway write. Holds the epoch ms the
  // operator clicked "Reconhecer incidente"; cleared after ACK_TTL_MS.
  const [ackedAt, setAckedAt] = useState<number | null>(null);

  useEffect(() => {
    if (ackedAt === null) return;
    const remaining = ACK_TTL_MS - (Date.now() - ackedAt);
    if (remaining <= 0) {
      setAckedAt(null);
      return;
    }
    const timer = setTimeout(() => setAckedAt(null), remaining);
    return () => clearTimeout(timer);
  }, [ackedAt]);

  const fsmState = data?.fsm_state;
  const critical = isCriticalFsmState(fsmState);
  const warning = isWarningFsmState(fsmState);

  // Nothing to show, or the operator acknowledged within the TTL.
  if (!critical && !warning) return null;
  if (ackedAt !== null && Date.now() - ackedAt < ACK_TTL_MS) return null;

  const title = critical
    ? "GPU primária indisponível. Failover ativo."
    : "Saturação sustentada / taxa de erro elevada.";
  const description = critical
    ? `Estado de failover: ${fsmState}. As chamadas seguem respondendo pelo upstream de fallback.`
    : `Estado atual: ${fsmState}. O gateway está degradado — monitorando latência e taxa de erro.`;

  return (
    <Alert
      role="alert"
      variant={critical ? "destructive" : "default"}
      // Sticky full-width banner, 44px min-height (Layout Constraints),
      // `sm`/8px internal padding.
      className={cn(
        "sticky top-0 z-30 flex min-h-[44px] w-full items-center gap-2 rounded-none border-x-0 border-t-0 p-2",
        critical
          ? "border-destructive/40 bg-destructive/15"
          : "border-status-warning/40 bg-status-warning/15",
      )}
    >
      <AlertTriangle
        className={cn(
          "size-4 shrink-0",
          critical ? "text-destructive" : "text-status-warning",
        )}
      />
      <div className="flex min-w-0 flex-1 flex-col">
        <AlertTitle
          className={cn(
            "text-[14px] font-semibold",
            critical ? "text-destructive" : "text-status-warning",
          )}
        >
          {title}
        </AlertTitle>
        <AlertDescription className="text-[12px]">
          {description}
        </AlertDescription>
      </div>
      <Button
        size="sm"
        variant={critical ? "destructive" : "outline"}
        onClick={() => setAckedAt(Date.now())}
      >
        Reconhecer incidente
      </Button>
    </Alert>
  );
}
