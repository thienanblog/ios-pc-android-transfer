// Package web holds the page phones and the computer open in the browser.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Files is the static site, rooted at the static folder.
func Files() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
