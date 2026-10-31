// Package version holds the build's version string.
//
// It is set at link time, the way the other house services do it:
//
//	go build -ldflags "-X github.com/AutumnsGrove/cairn/internal/version.Version=r1.2.3"
//
// "dev" means a local build. Nothing else reads this; the health reply carries it so the update watcher and
// a running client can tell which binary is answering (docs/UPDATE.md).
package version

// Version is the build's version. The link-time default is for local builds only.
var Version = "dev"
