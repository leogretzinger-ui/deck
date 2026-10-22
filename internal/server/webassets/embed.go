// Package webassets owns the generated production frontend embedded in the
// Deck binary. The Makefile stages Vite's output below this package
// before compiling Go; the checked-in UNBUILT marker only keeps plain Go test
// and development builds compilable without requiring Node.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// FS returns the frontend filesystem rooted at Vite's output directory.
func FS() fs.FS {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		// The path is a compile-time go:embed invariant, not runtime input.
		panic("webassets: embedded dist subtree is unavailable: " + err.Error())
	}
	return dist
}
