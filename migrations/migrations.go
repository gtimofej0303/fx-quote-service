package migrations

import "embed"

// FS contains versioned SQL migrations compiled into the migrate binary.
//
//go:embed *.sql
var FS embed.FS
