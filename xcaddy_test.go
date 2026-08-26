// Package rulefiles_test checks that the handlers of this module plug into a Caddy
// build next to the standard directives they are used with. The built-in header
// directive owns the http.handlers.headers module ID, so registering headers_file
// next to it has to stay possible.
package rulefiles_test

import (
	"bytes"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig"
	_ "github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	_ "thde.io/caddy-rulefiles/headersfile"
	_ "thde.io/caddy-rulefiles/redirectsfile"
)

func TestCaddyfileAdapt(t *testing.T) {
	t.Parallel()

	// A bare file argument would be taken for a path matcher, hence the "*".
	config := `:8080 {
	header X-Served-By nine
	headers_file * /srv/_headers
	redirects_file * /srv/_redirects
}
`

	adapter := caddyconfig.GetAdapter("caddyfile")
	if adapter == nil {
		t.Fatal(`caddyconfig.GetAdapter("caddyfile") = nil`)
	}

	adapted, warnings, err := adapter.Adapt([]byte(config), nil)
	if err != nil {
		t.Fatalf("Adapt() error = %v", err)
	}
	for _, warning := range warnings {
		t.Errorf("Adapt() warning = %s", warning.Message)
	}

	// The handlers have to end up in the order their directives are registered in:
	// response headers first, the redirects after them but still before anything
	// rewrites the requested path.
	order := []string{"headers", "headers_file", "redirects_file"}
	previous, previousAt := "", -1
	for _, handler := range order {
		at := indexOf(adapted, handler)
		if at < 0 {
			t.Fatalf("adapted config is missing the %s handler:\n%s", handler, adapted)
		}
		if at < previousAt {
			t.Errorf("%s is applied before %s:\n%s", handler, previous, adapted)
		}
		previous, previousAt = handler, at
	}
}

func TestCaddyfileAdaptErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"headers_file without a file":   "headers_file *",
		"redirects_file without a file": "redirects_file *",
	}

	for name, directive := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := ":8080 {\n\t" + directive + "\n}\n"
			if _, _, err := caddyconfig.GetAdapter("caddyfile").Adapt([]byte(config), nil); err == nil {
				t.Fatalf("Adapt(%q) error = nil, want an error", directive)
			}
		})
	}
}

// indexOf returns the position of the handler in the adapted config.
func indexOf(adapted []byte, name string) int {
	return bytes.Index(adapted, []byte(`"handler":"`+name+`"`))
}
