// Package web embeds the static frontend assets served by the dashboard.
package web

import (
	"embed"
	"io/fs"
)

//go:embed assets
var embedded embed.FS

// FS returns the frontend asset filesystem rooted so that index.html is at "/".
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
