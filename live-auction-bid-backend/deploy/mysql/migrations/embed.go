package migrations

import (
	"embed"
	"io/fs"
)

// files contains the versioned schema migrations shipped with the migrate binary.
//
//go:embed *.up.sql *.down.sql
var files embed.FS

// Open returns the embedded migration filesystem rooted at this directory.
func Open() (fs.FS, error) {
	return fs.Sub(files, ".")
}
