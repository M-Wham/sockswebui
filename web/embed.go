// Package web embeds the built Svelte UI (web/dist) into the binary.
package web

import (
	"embed"
	"io/fs"
)

// all: is required so the dotfile placeholder dist/.keep is embedded when
// the UI has not been built; otherwise the pattern matches no files.
//
//go:embed all:dist
var dist embed.FS

// DistFS returns the embedded UI rooted at web/dist.
func DistFS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub only fails for an invalid path; "dist" is a constant valid path.
		panic(err)
	}
	return sub
}
