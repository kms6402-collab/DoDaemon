// Package webui serves the embedded management dashboard: server status,
// a live event/log stream (SSE), and (later) config editing — all bundled
// into the binary via embed.FS so no external assets are needed at runtime
// (docs/PLAN.md §4.6/§5.6/§7).
package webui

import (
	"embed"
	"html/template"
)

//go:embed static
var staticFS embed.FS

//go:embed templates/*.html
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))
