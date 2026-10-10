// Package seeds holds LOCAL DEMO data. All names and positions are fictional samples (داده ساختگی)
// for exercises; they are not an authoritative facility register.
package seeds

import _ "embed"

//go:embed dev.sql
var Dev string

// GuidanceFA is the sample assistant guidance (approved as version 1 for the demo; needs expert review).
// The citizen app bundles an identical copy for offline use (checked by a test).
//
//go:embed guidance.fa.json
var GuidanceFA []byte
