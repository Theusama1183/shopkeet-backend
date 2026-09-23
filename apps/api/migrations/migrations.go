// Package migrations embeds the SQL migration files so they ship inside the
// compiled binary (golang-migrate iofs source). One file per change,
// sequentially numbered — never edit an already-applied migration.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS