// Package seeds holds LOCAL DEMO data. All names and positions are fictional samples (داده ساختگی)
// for exercises; they are not an authoritative facility register.
package seeds

import _ "embed"

//go:embed dev.sql
var Dev string
