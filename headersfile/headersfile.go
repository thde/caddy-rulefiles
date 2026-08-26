// Package headersfile implements a Caddy HTTP handler that serves the response
// headers of a _headers file, the format popularised by Netlify and Cloudflare
// Pages.
package headersfile

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"thde.io/rulefiles/header"
)

func init() {
	caddy.RegisterModule(Handler{})
	httpcaddyfile.RegisterHandlerDirective("headers_file", parseCaddyfile)
	// Rules match the requested path, so they have to be evaluated before a
	// rewrite or try_files changes it. That is where the header directive sets
	// response headers as well.
	httpcaddyfile.RegisterDirectiveOrder("headers_file", httpcaddyfile.After, "header")
}

// Handler serves the response headers of one or more _headers files. The files
// are read once while provisioning, changing them requires a config reload.
//
// All rules matching a request apply, in the order they are declared. A field
// declared by more than one rule is joined with a comma, a "!" rule removes what
// the rules before it declared as well as what a later handler sets:
//
//	/*
//	  X-Robots-Tag: noindex
//	/docs/*
//	  ! X-Robots-Tag
//
// The rules are matched against the path as it was requested, before try_files or
// a rewrite changes it, and are applied to the redirects of redirects_file as well
// as to the files of file_server.
type Handler struct {
	// Files are the _headers files to load, in order of precedence.
	Files []string `json:"files,omitempty"`

	rules  []header.Rule
	logger *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.headers_file",
		New: func() caddy.Module { return new(Handler) },
	}
}

// UnmarshalCaddyfile parses the headers_file directive:
//
//	headers_file [<matcher>] <file...>
//
// A leading argument that starts with "/" is taken for a path matcher, so the
// catch-all matcher has to be spelled out for a file:
//
//	headers_file * /srv/_headers
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		files := d.RemainingArgs()
		if len(files) == 0 {
			return d.Err(`expected a file: a leading path is taken for a matcher, write "headers_file * <file...>"`)
		}
		h.Files = append(h.Files, files...)

		if d.NextBlock(0) {
			return d.Errf("unrecognized subdirective %q", d.Val())
		}
	}

	return nil
}

// errNoFile is returned by Validate for a handler without a file.
var errNoFile = errors.New("no _headers file configured")

// Validate ensures the handler is configured with at least one file.
func (h *Handler) Validate() error {
	if len(h.Files) == 0 {
		return errNoFile
	}

	return nil
}

// Provision loads the rules of all configured files.
func (h *Handler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger()
	repl := caddy.NewReplacer()

	for _, file := range h.Files {
		name := repl.ReplaceKnown(file, "")

		loaded, err := parseFile(name)
		if err != nil {
			return err
		}

		h.logger.Info("loaded headers", zap.String("file", name), zap.Int("rules", len(loaded)))
		h.rules = append(h.rules, loaded...)
	}

	return nil
}

// parseFile reads the rules of a single _headers file.
func parseFile(name string) ([]header.Rule, error) {
	//nolint:gosec // G304: the name is part of the server config, which is trusted.
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("opening headers file: %w", err)
	}
	defer f.Close()

	rules, err := header.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parsing headers file %s:\n%w", name, err)
	}

	return rules, nil
}

// ServeHTTP applies the fields of all rules matching the request to the response.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	resolved := header.Resolve(h.rules, r.URL.Path)
	if resolved == nil {
		return next.ServeHTTP(w, r)
	}
	// A rule for "/*" matches every request, so the fields are only collected if
	// they are logged.
	if c := h.logger.Check(zapcore.DebugLevel, "matched headers"); c != nil {
		c.Write(zap.String("path", r.URL.Path), zap.Strings("fields", resolved.Fields()))
	}

	// The fields are applied once the response headers are final, so that they
	// win over the ones a later handler such as file_server sets.
	return next.ServeHTTP(&responseWriter{
		ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w},
		resolved:              resolved,
	}, r)
}

// responseWriter defers the resolved header fields until the response headers
// are written.
type responseWriter struct {
	*caddyhttp.ResponseWriterWrapper

	resolved    *header.Resolved
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(status int) {
	if rw.wroteHeader {
		return
	}
	// 1xx responses are informational, the headers of the response itself are
	// still to come.
	if status < 100 || status > 199 {
		rw.wroteHeader = true
		rw.resolved.ApplyTo(rw.Header())
	}

	rw.ResponseWriterWrapper.WriteHeader(status)
}

func (rw *responseWriter) Write(p []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}

	return rw.ResponseWriterWrapper.Write(p)
}

// ReadFrom keeps the promoted io.ReaderFrom of the wrapper, which file_server
// copies a file through, from writing the response headers unchanged.
func (rw *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}

	return rw.ResponseWriterWrapper.ReadFrom(r)
}

// parseCaddyfile sets up the handler from the headers_file directive.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var handler Handler
	if err := handler.UnmarshalCaddyfile(h.Dispenser); err != nil {
		return nil, err
	}

	return &handler, nil
}

// Interface guards.
var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddy.Validator             = (*Handler)(nil)
	_ caddyfile.Unmarshaler       = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
	_ http.ResponseWriter         = (*responseWriter)(nil)
	_ io.ReaderFrom               = (*responseWriter)(nil)
)
