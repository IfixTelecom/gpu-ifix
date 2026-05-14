"use client";

/**
 * "Atualizado há {n}s" stale-data indicator (UI-SPEC §Copywriting) — shows
 * how long ago the last successful React Query refetch landed. Sits next to
 * the page title.
 *
 * WR-07: `seconds` is computed from `Date.now()` AT RENDER TIME, not from a
 * value captured by the `setInterval` callback. A backgrounded tab throttles
 * `setInterval` (so it fires less often), but `Date.now()` is never
 * throttled — only the re-render frequency is. A stale render showing a
 * fresh `Date.now() - updatedAt` is still correct, which is exactly what a
 * staleness indicator must be when the tab is backgrounded. The interval
 * here exists ONLY to trigger periodic re-renders; the `tick` state value
 * is intentionally unused.
 */

import { useEffect, useState } from "react";

export interface StaleIndicatorProps {
  /** `dataUpdatedAt` from the `useQuery` result — epoch ms, 0 if never. */
  updatedAt: number;
}

export function StaleIndicator({ updatedAt }: StaleIndicatorProps) {
  // `tick` is a re-render trigger only — the displayed value is derived
  // from Date.now() below, never from this state.
  const [, setTick] = useState(0);

  useEffect(() => {
    const timer = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(timer);
  }, []);

  if (!updatedAt) return null;

  // Read wall time at render — never a throttled setInterval-captured now.
  const seconds = Math.max(0, Math.round((Date.now() - updatedAt) / 1000));

  return (
    <span className="text-[12px] font-semibold text-muted-foreground tabular-nums">
      Atualizado há {seconds}s
    </span>
  );
}
