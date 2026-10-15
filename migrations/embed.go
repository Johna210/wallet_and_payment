// Package migrations embeds the versioned goose SQL migrations so the binary
// can apply them without the source tree.
package migrations

import "embed"

// FS holds every migration file in this directory.
//
//go:embed *.sql
var FS embed.FS
