// Package migrations embeds the ordered SQL migrations applied by cmd/migrate and the API at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
