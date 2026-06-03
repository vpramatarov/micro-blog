// Package web embeds the built React single-page app (the Vite `dist` output)
// into the Go binary so the server is fully self-contained — mirroring the
// embed pattern used for migrations (cmd/embed.go) and the OpenAPI spec
// (api/embed.go).
//
// The `all:` prefix includes files whose names start with `.` or `_` (some
// build outputs do). A committed placeholder dist/index.html guarantees this
// package compiles before the frontend has ever been built; `npm run build`
// (locally or in the Docker web stage) overwrites dist with the real bundle.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// Dist returns the embedded build rooted at the dist directory, ready to hand
// to the SPA handler / router.
func Dist() (fs.FS, error) {
	return fs.Sub(assets, "dist")
}
