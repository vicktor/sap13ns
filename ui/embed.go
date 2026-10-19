// Package ui embeds every template, stylesheet, script and translation so
// that the sap13ns binary is self-contained.
package ui

import "embed"

// Themes are embedded whole (their src/ CSS is small) so that a theme that is
// still missing its built static/ directory does not break the build.
//
//go:embed admin/templates network/templates static shared/templates i18n themes
var FS embed.FS
