package migrations

import "embed"

// FS holds the SQL files used to update the database.
//
//go:embed *.sql
var FS embed.FS
