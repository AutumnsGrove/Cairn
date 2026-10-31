// Package webui holds the built frontend, embedded into the binary.
//
// The build is made by `make web` and copied into dist/, which is ignored by git apart from the placeholder.
// Only the placeholder is ever committed, so a stale bundle can never be mistaken for source
// (docs/CI.md 4.6, the guard job).
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded frontend rooted at the directory that holds index.html. When the frontend has not
// been built, the filesystem is effectively empty and the server serves its placeholder page instead.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: the directive above fails the build if dist/ is missing entirely.
		panic("webui: embedded frontend is not readable: " + err.Error())
	}
	return sub
}
