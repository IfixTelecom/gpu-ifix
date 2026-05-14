"use client";

/**
 * React Query provider for the dashboard.
 *
 * CONTEXT.md / UI-SPEC lock the dashboard refresh model to React Query
 * POLLING (not WebSocket). `refetchInterval` is pinned to 7000ms — inside the
 * UI-SPEC 5–10s band — so every `useQuery` against the gateway proxy refetches
 * on its own without per-component config. `refetchOnWindowFocus` keeps the
 * screen current when an operator tabs back to it.
 */

import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { useState } from "react";

/** The shared 5–10s poll interval — referenced by the stale-data indicator. */
export const GATEWAY_POLL_INTERVAL_MS = 7000;

export function QueryProvider({ children }: { children: React.ReactNode }) {
  // One client per browser session — created lazily so it is never shared
  // across requests on the server.
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // UI-SPEC §refresh — poll the gateway every 5–10s.
            refetchInterval: GATEWAY_POLL_INTERVAL_MS,
            refetchOnWindowFocus: true,
            // A short stale window so the "Atualizado há {n}s" indicator
            // reflects the real poll cadence.
            staleTime: GATEWAY_POLL_INTERVAL_MS,
            retry: 1,
          },
        },
      }),
  );

  return (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}
