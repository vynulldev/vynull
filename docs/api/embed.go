// SPDX-License-Identifier: GPL-3.0-or-later

// Package apidocs embeds the OpenAPI specification and a self-hosted Swagger UI
// so the HTTP API can serve its own browsable documentation with no external
// (CDN) dependency — the same offline-first stance as the rest of the UI.
//
// The Swagger UI assets under swagger/ are vendored from swagger-ui-dist and
// are licensed Apache-2.0 (see swagger/LICENSE), which is compatible with this
// project's GPLv3.
package apidocs

import "embed"

// Spec is the OpenAPI 3 document (YAML).
//
//go:embed openapi.yaml
var Spec []byte

// DocsHTML is the Swagger UI host page; it loads the vendored assets and points
// at /api/openapi.yaml.
//
//go:embed docs.html
var DocsHTML []byte

// SwaggerFS holds the vendored Swagger UI assets (served under /api/swagger/).
//
//go:embed swagger/swagger-ui.css swagger/swagger-ui-bundle.js
var SwaggerFS embed.FS
