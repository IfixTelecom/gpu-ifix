import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CriticalBanner } from "@/components/critical-banner";
import type { MetricsResponse } from "@/lib/gateway";

/**
 * The critical banner is the UI-SPEC's primary focal point. It must:
 *  - render the RED destructive banner on a FAILED_OVER fsm_state
 *  - render NOTHING on a HEALTHY fsm_state
 *  - collapse when "Reconhecer incidente" is clicked (local state only)
 *
 * `fetchMetrics` is mocked so no proxy/network call happens.
 */

const { fetchMetricsMock } = vi.hoisted(() => ({
  fetchMetricsMock: vi.fn(),
}));

vi.mock("@/lib/gateway", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/gateway")>();
  return { ...actual, fetchMetrics: fetchMetricsMock };
});

function metricsWith(fsmState: string): MetricsResponse {
  return {
    window: "5m",
    generated_at: "2026-05-14T09:00:00Z",
    tenants: [],
    by_route: [],
    by_upstream: [],
    inflight: 0,
    fsm_state: fsmState,
  };
}

function renderBanner() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(<CriticalBanner />, { wrapper });
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("CriticalBanner", () => {
  it("renders the red critical banner on a FAILED_OVER fsm_state", async () => {
    fetchMetricsMock.mockResolvedValue(metricsWith("FAILED_OVER"));
    renderBanner();

    const alert = await screen.findByRole("alert");
    // The destructive variant + the explicit destructive-tinted background
    // are how the RED critical banner is identified.
    expect(alert.className).toContain("bg-destructive/15");
    expect(
      screen.getByText(/GPU primária indisponível/i),
    ).toBeInTheDocument();
  });

  it("renders nothing on a HEALTHY fsm_state", async () => {
    fetchMetricsMock.mockResolvedValue(metricsWith("HEALTHY"));
    renderBanner();

    await waitFor(() => expect(fetchMetricsMock).toHaveBeenCalled());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("collapses the banner when 'Reconhecer incidente' is clicked", async () => {
    fetchMetricsMock.mockResolvedValue(metricsWith("FAILED_OVER"));
    renderBanner();

    const alert = await screen.findByRole("alert");
    expect(alert).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: /Reconhecer incidente/i }),
    );

    await waitFor(() =>
      expect(screen.queryByRole("alert")).not.toBeInTheDocument(),
    );
  });
});
