// Wave 0 scaffolding file (Phase 3, plan 03-01). This file pins the
// downstream dependencies that Phase 3 waves 2+ will use, so `go mod
// tidy` does not strip them from go.mod before the implementation
// lands. Each subsequent plan in this phase deletes one entry from
// this file as it adds the real code that imports the dep:
//
//   - sony/gobreaker/v2  → consumed by gateway/internal/breaker/breaker.go (DONE in 03-03)
//   - cenkalti/backoff/v5 → consumed by gateway/internal/proxy/dispatcher.go
//   - jackc/pgxlisten     → consumed by gateway/internal/upstreams/listen.go
//
// When all three real consumers exist, this file MUST be deleted.

package breaker

import (
	_ "github.com/cenkalti/backoff/v5"
	_ "github.com/jackc/pgxlisten"
)
