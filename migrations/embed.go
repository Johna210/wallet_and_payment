// Package migrations embeds the versioned goose SQL migrations so the binary
// can apply them without the source tree.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
