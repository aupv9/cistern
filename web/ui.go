//go:build ui

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist/client
var dist embed.FS

func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist/client")
	if err != nil {
		panic(err)
	}
	return sub
}

const Built = true
