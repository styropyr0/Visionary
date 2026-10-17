// Package web embeds the built analytics frontend. dist is produced by
// `npm run build` in web/analytics; the committed .gitkeep keeps the embed
// valid before the first build.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built frontend with index.html at its root.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
