// Package web embeds the built dashboard so the server ships as one binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// DistFS returns the built UI rooted at the dist directory.
func DistFS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this cannot fail at runtime
	}
	return sub
}
