// Package web embeds Lotus's templates and static assets into the binary
// for production. In dev mode (LOTUS_DEV=1) callers read straight from disk
// instead, so template/CSS edits show up without a rebuild — see
// internal/httpx.NewRenderer.
package web

import "embed"

//go:embed templates
var Templates embed.FS

//go:embed static
var Static embed.FS
