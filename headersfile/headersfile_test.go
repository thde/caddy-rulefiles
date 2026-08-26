package headersfile

import (
	"io"
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
# hardening for everything
/*
	X-Content-Type-Options: nosniff
	X-Robots-Tag: noindex

/assets/*
	Cache-Control: public, max-age=31536000, immutable
	! X-Robots-Tag
`

func TestHandlerServeHTTP(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		request string
		// serve is what the next handler does with the response.
		serve func(http.ResponseWriter) error
		// want holds the fields expected on the response, "-" for an absent one.
		want map[string]string
	}{
		"headers of a written response": {
			request: "/index.html",
			serve: func(w http.ResponseWriter) error {
				_, err := w.Write([]byte("<html>"))
				return err
			},
			want: map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Robots-Tag":           "noindex",
			},
		},
		"a rule replaces what the next handler set": {
			request: "/index.html",
			serve: func(w http.ResponseWriter) error {
				w.Header().Set("X-Robots-Tag", "index")
				w.WriteHeader(http.StatusOK)

				return nil
			},
			want: map[string]string{"X-Robots-Tag": "noindex"},
		},
		"a removal drops what the next handler set": {
			request: "/assets/main.css",
			serve: func(w http.ResponseWriter) error {
				w.Header().Set("X-Robots-Tag", "index")
				w.WriteHeader(http.StatusOK)

				return nil
			},
			want: map[string]string{
				"X-Robots-Tag":  "-",
				"Cache-Control": "public, max-age=31536000, immutable",
			},
		},
		"headers of a copied response": {
			request: "/assets/main.css",
			serve: func(w http.ResponseWriter) error {
				_, err := io.Copy(w, strings.NewReader("body {}"))
				return err
			},
			want: map[string]string{"Cache-Control": "public, max-age=31536000, immutable"},
		},
		"headers of an error response": {
			request: "/missing",
			serve: func(w http.ResponseWriter) error {
				w.WriteHeader(http.StatusNotFound)

				return nil
			},
			want: map[string]string{"X-Robots-Tag": "noindex"},
		},
		"the query is not matched on": {
			request: "/assets/main.css?v=2",
			want:    map[string]string{"Cache-Control": "public, max-age=31536000, immutable"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := testHandler(t, testFile)
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
				if test.serve == nil {
					w.WriteHeader(http.StatusOK)

					return nil
				}

				return test.serve(w)
			})

			rec := httptest.NewRecorder()
			if err := handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.request, http.NoBody), next); err != nil {
				t.Fatalf("ServeHTTP() error = %v", err)
			}

			for name, want := range test.want {
				got := rec.Result().Header.Get(name)
				if want == "-" {
					if got != "" {
						t.Errorf("%s = %q, want it to be absent", name, got)
					}
					continue
				}
				if got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestHandlerServeHTTPUnmatched(t *testing.T) {
	t.Parallel()

	handler := testHandler(t, "/assets/*\n\tX-A: 1\n")
	var served http.ResponseWriter
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		served = w

		return nil
	})

	rec := httptest.NewRecorder()
	if err := handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/index.html", http.NoBody), next); err != nil {
		t.Fatalf("ServeHTTP() error = %v", err)
	}

	if served != rec {
		t.Errorf("next handler served %T, want the response not to be wrapped", served)
	}
}

func TestHandlerServeHTTPMatchesBeforeARewrite(t *testing.T) {
	t.Parallel()

	// try_files rewrites a request to /index.html before file_server serves it.
	// The rules have to match the path that was requested.
	handler := testHandler(t, "/docs/*\n\tX-A: 1\n")
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		r.URL.Path = "/index.html"
		w.WriteHeader(http.StatusOK)

		return nil
	})

	rec := httptest.NewRecorder()
	if err := handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/docs/backups", http.NoBody), next); err != nil {
		t.Fatalf("ServeHTTP() error = %v", err)
	}

	if got, want := rec.Result().Header.Get("X-A"), "1"; got != want {
		t.Errorf("X-A = %q, want %q", got, want)
	}
}

func TestHandlerProvisionErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()

		handler := Handler{Files: []string{filepath.Join(t.TempDir(), "_headers")}}
		if err := handler.Provision(caddy.Context{}); err == nil {
			t.Fatal("Provision() error = nil, want an error for a missing file")
		}
	})

	t.Run("invalid file", func(t *testing.T) {
		t.Parallel()

		name := writeFile(t, "/*\n\tX-Robots-Tag\n")
		handler := Handler{Files: []string{name}}

		err := handler.Provision(caddy.Context{})
		if err == nil {
			t.Fatal("Provision() error = nil, want an error for an invalid header")
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
		"single file":          {config: "headers_file /srv/_headers", want: []string{"/srv/_headers"}},
		"multiple files":       {config: "headers_file /srv/_headers /etc/caddy/_headers", want: []string{"/srv/_headers", "/etc/caddy/_headers"}},
		"missing file":         {config: "headers_file", wantErr: true},
		"unknown subdirective": {config: "headers_file /srv/_headers {\n\tdefer\n}", wantErr: true},
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

// testHandler returns a provisioned handler serving the given _headers file.
func testHandler(t *testing.T, file string) *Handler {
	t.Helper()

	handler := &Handler{Files: []string{writeFile(t, file)}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	return handler
}

// writeFile writes a _headers file to a temporary directory.
func writeFile(t *testing.T, content string) string {
	t.Helper()

	name := filepath.Join(t.TempDir(), "_headers")
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	return name
}
