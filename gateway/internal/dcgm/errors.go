// Package dcgm (errors.go): sentinel errors for the DCGM HTTP scraper
// introduced by Phase 5 (CONTEXT.md D-A3).
//
// The scraper periodically GETs the pod's :9400/metrics endpoint and
// parses Prometheus text format to extract DCGM_FI_DEV_FB_USED (MiB).
// Failures are non-fatal — fail-open: VRAM signal is dropped from the
// 2-of-3 saturation composite while scrape errors persist, and shed
// effectively reduces to inflight+P95 majority-OR.
package dcgm

import "errors"

var (
	// ErrDCGMScrapeFailed wraps any non-fatal scrape failure (http error,
	// non-200 status, parse error, missing metric, wrong metric type).
	// The scraper continues to run (CONTEXT D-A3 fail-open).
	ErrDCGMScrapeFailed = errors.New("dcgm: scrape failed")

	// ErrDCGMUnitMismatch: defensive check that DCGM_FI_DEV_FB_USED help
	// text declares MiB (RESEARCH Pitfall 1). Logged once at startup if
	// the metric family parses but the unit cannot be confirmed.
	ErrDCGMUnitMismatch = errors.New("dcgm: metric unit mismatch (expected MiB)")
)
