// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestAPIError checks the CLI shows an API error's message, and the body as
// text when the answer isn't the API's JSON error.
func TestAPIError(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{404, `{"error":"track not found"}` + "\n", "404 Not Found: track not found"},
		{416, "invalid range\n", "416 Requested Range Not Satisfiable: invalid range"},
		{500, `{"other":1}`, `500 Internal Server Error: {"other":1}`},
	} {
		resp := &http.Response{
			StatusCode: c.status,
			Status:     fmt.Sprintf("%d %s", c.status, http.StatusText(c.status)),
			Body:       io.NopCloser(strings.NewReader(c.body)),
		}
		if got := apiError(resp).Error(); got != c.want {
			t.Errorf("%d %q: %q, want %q", c.status, c.body, got, c.want)
		}
	}
}
