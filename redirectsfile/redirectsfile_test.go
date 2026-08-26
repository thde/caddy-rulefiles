package redirectsfile

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

const testFile = `
# legacy article URLs
/a/675dzGXvtlQ    /docs/on-demand-services
/article/*        /docs/:splat
/artikel/:slug    /de/docs/:slug          302
/removed          /404.html               410
/spa/*            /index.html             200
/external         https://example.com/x   308
`

func TestHandlerServeHTTP(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		request      string
		wantStatus   int
		wantLocation string
		wantNextPath string // the path the next handler is expected to serve
	}{
		"exact redirect": {
			request:      "/a/675dzGXvtlQ",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/docs/on-demand-services",
		},
		"splat redirect": {
			request:      "/article/backups",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/docs/backups",
		},
		"placeholder redirect": {
			request:      "/artikel/sicherungen",
			wantStatus:   http.StatusFound,
			wantLocation: "/de/docs/sicherungen",
		},
		"query is kept": {
			request:      "/article/backups?locale=de",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/docs/backups?locale=de",
		},
		"escaped path": {
			request:      "/article/a%20b",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/docs/a%20b",
		},
		"external redirect": {
			request:      "/external",
			wantStatus:   http.StatusPermanentRedirect,
			wantLocation: "https://example.com/x",
		},
		"rewrite": {
			request:      "/spa/deep/link",
			wantNextPath: "/index.html",
		},
		"unmatched request": {
			request:      "/docs/backups",
			wantNextPath: "/docs/backups",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := testHandler(t, testFile)

			var nextPath string
			next := caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
				nextPath = r.URL.Path
				return nil
			})

			rec := httptest.NewRecorder()
			err := handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.request, http.NoBody), next)
			if err != nil {
				t.Fatalf("ServeHTTP() error = %v", err)
			}

			if nextPath != test.wantNextPath {
				t.Errorf("next handler served %q, want %q", nextPath, test.wantNextPath)
			}
			if test.wantNextPath != "" {
				return
			}
			if rec.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, test.wantStatus)
			}
			if got := rec.Header().Get("Location"); got != test.wantLocation {
				t.Errorf("Location = %q, want %q", got, test.wantLocation)
			}
		})
	}
}

func TestHandlerServeHTTPError(t *testing.T) {
	t.Parallel()

	handler := testHandler(t, testFile)
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Error("next handler called, want the request to be answered by the rule")
		return nil
	})

	err := handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/removed", http.NoBody), next)

	var handlerErr caddyhttp.HandlerError
	if !errors.As(err, &handlerErr) {
		t.Fatalf("ServeHTTP() error = %v, want a caddyhttp.HandlerError", err)
	}
	if handlerErr.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want %d", handlerErr.StatusCode, http.StatusGone)
	}
}

func TestHandlerFirstMatchWins(t *testing.T) {
	t.Parallel()

	handler := testHandler(t, "/article/backups /docs/object-storage/backups\n/article/* /docs/:splat\n")

	rec := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil })
	if err := handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/article/backups", http.NoBody), next); err != nil {
		t.Fatalf("ServeHTTP() error = %v", err)
	}

	if got, want := rec.Header().Get("Location"), "/docs/object-storage/backups"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestHandlerProvisionErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()

		handler := Handler{Files: []string{filepath.Join(t.TempDir(), "_redirects")}}
		if err := handler.Provision(caddy.Context{}); err == nil {
			t.Fatal("Provision() error = nil, want an error for a missing file")
		}
	})

	t.Run("invalid file", func(t *testing.T) {
		t.Parallel()

		name := writeFile(t, "/old\n")
		handler := Handler{Files: []string{name}}

		err := handler.Provision(caddy.Context{})
		if err == nil {
			t.Fatal("Provision() error = nil, want an error for an invalid rule")
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("Provision() error = %v, want it to name %s", err, name)
		}
	})
}

func TestHandlerUnmarshalCaddyfile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		config  string
		want    []string
		wantErr bool
	}{
		"single file":          {config: "redirects_file /srv/_redirects", want: []string{"/srv/_redirects"}},
		"multiple files":       {config: "redirects_file /srv/_redirects /etc/caddy/_redirects", want: []string{"/srv/_redirects", "/etc/caddy/_redirects"}},
		"missing file":         {config: "redirects_file", wantErr: true},
		"unknown subdirective": {config: "redirects_file /srv/_redirects {\n\tstatus 302\n}", wantErr: true},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var handler Handler
			err := handler.UnmarshalCaddyfile(caddyfile.NewTestDispenser(test.config))
			if gotErr := err != nil; gotErr != test.wantErr {
				t.Fatalf("UnmarshalCaddyfile() error = %v, want error = %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if strings.Join(handler.Files, " ") != strings.Join(test.want, " ") {
				t.Errorf("Files = %v, want %v", handler.Files, test.want)
			}
		})
	}
}

// testHandler returns a provisioned handler serving the given _redirects file.
func testHandler(t *testing.T, file string) *Handler {
	t.Helper()

	handler := &Handler{Files: []string{writeFile(t, file)}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	return handler
}

// writeFile writes a _redirects file to a temporary directory.
func writeFile(t *testing.T, content string) string {
	t.Helper()

	name := filepath.Join(t.TempDir(), "_redirects")
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	return name
}
