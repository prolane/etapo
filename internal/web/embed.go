package web

import (
	"embed"
	"io/fs"
)

//go:embed static/*
var staticFS embed.FS

// GetFS returns the fs.FS scoped to the static folder.
func GetFS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
