//go:build phase5_tools

// Package shed (tools_phase5.go): Wave-0 build-tag-isolated module graph
// pin so go.mod records the Phase 5 deps as direct (not indirect) before
// the real importers land in Plan 02 (config/types/mirror), Plan 03 (DCGM
// scraper imports prometheus/common/expfmt) and Plan 06 (integration
// tests import tsenart/vegeta/lib).
//
// The build tag `phase5_tools` is never set during normal builds — this
// file contributes ONLY to the module graph, not the binary. When the
// real importers ship, this file becomes redundant and is removed by
// the same plan that introduces them.
//
// Pattern reference: https://go.dev/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module
package shed

import (
	_ "github.com/prometheus/common/expfmt"
	_ "github.com/tsenart/vegeta/lib"
)
