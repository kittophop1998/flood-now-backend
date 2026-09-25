// Package migrations embeds the SQL migration files so cmd/migrate can run
// them without depending on a local psql client or the source tree being
// present at runtime (e.g. inside a deployed container).
package migrations

import "embed"

//go:embed *.up.sql *.down.sql
var FS embed.FS
