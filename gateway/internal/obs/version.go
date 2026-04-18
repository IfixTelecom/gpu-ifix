// Package obs (version.go): build-time version string surfaced in the
// /health JSON body and in Sentry's Release tag.
package obs

// BuildVersion is set at build time via -ldflags "-X ...=vX.Y.Z".
// Default "dev" is used when the binary is built without explicit tagging.
// Plan 08 build-gateway.yml populates this from `${TAG}`.
var BuildVersion = "dev"
