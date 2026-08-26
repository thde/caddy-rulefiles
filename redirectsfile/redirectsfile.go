// Package redirectsfile implements a Caddy HTTP handler that serves the redirects
// of a _redirects file, the format popularised by Netlify and Cloudflare Pages.
package redirectsfile

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"thde.io/rulefiles/redirect"
)

func init() {
	caddy.RegisterModule(Handler{})
	httpcaddyfile.RegisterHandlerDirective("redirects_file", parseCaddyfile)
	// Redirects have to be evaluated before a request is rewritten by
	// try_files or answered by file_server, the same place redir is handled at.
	httpcaddyfile.RegisterDirectiveOrder("redirects_file", httpcaddyfile.After, "redir")
}

// Handler serves the redirects of one or more _redirects files. The files are
// read once while provisioning, changing them requires a config reload.
//
// Rules are matched in the order they are declared, the first match wins.
// Unlike on Netlify, a rule always takes precedence over an existing file. Use
// the file matcher to give files precedence instead:
//
//	@missing not file
//	redirects_file @missing /srv/_redirects
//
// Without a matcher the catch-all one has to be spelled out, as a leading path is
// taken for a path matcher:
//
//	redirects_file * /srv/_redirects
type Handler struct {
	// Files are the _redirects files to load, in order of precedence.
	Files []string `json:"files,omitempty"`

	rules  []redirect.Rule
	logger *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.redirects_file",
		New: func() caddy.Module { return new(Handler) },
	}
}

// UnmarshalCaddyfile parses the redirects_file directive:
//
//	redirects_file [<matcher>] <file...>
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		files := d.RemainingArgs()
		if len(files) == 0 {
			return d.Err(`expected a file: a leading path is taken for a matcher, write "redirects_file * <file...>"`)
		}
		h.Files = append(h.Files, files...)

		if d.NextBlock(0) {
			return d.Errf("unrecognized subdirective %q", d.Val())
		}
	}

	return nil
}

// errNoFile is returned by Validate for a handler without a file.
var errNoFile = errors.New("no _redirects file configured")

// errStatus is served for a rule that answers with an error status itself.
var errStatus = errors.New("rule with an error status")

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

		h.logger.Info("loaded redirects", zap.String("file", name), zap.Int("rules", len(loaded)))
		h.rules = append(h.rules, loaded...)
	}

	return nil
}

// parseFile reads the rules of a single _redirects file.
func parseFile(name string) ([]redirect.Rule, error) {
	//nolint:gosec // G304: the name is part of the server config, which is trusted.
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("opening redirects file: %w", err)
	}
	defer f.Close()

	rules, err := redirect.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parsing redirects file %s:\n%w", name, err)
	}

	return rules, nil
}

// ServeHTTP serves the first rule matching the request. Requests without a
// matching rule are passed on to the next handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	resolved, err := redirect.Resolve(h.rules, r.URL)
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	if resolved == nil {
		return next.ServeHTTP(w, r)
	}
	rule, to := resolved.Rule, resolved.To

	if rule.Status >= 400 {
		return caddyhttp.Error(rule.Status, fmt.Errorf("%s is matched by %s: %w", r.URL.Path, rule.Source, errStatus))
	}
	h.logger.Debug(
		"matched redirect",
		zap.String("source", rule.Source),
		zap.String("path", r.URL.Path),
		zap.String("target", to.String()),
		zap.Int("status", rule.Status),
	)

	// A rewrite keeps the request URL and serves the target instead.
	if rule.Status == http.StatusOK {
		r.URL.Path, r.URL.RawPath, r.URL.RawQuery = to.Path, to.RawPath, to.RawQuery

		return next.ServeHTTP(w, r)
	}

	// The target host is the one of the rule, placeholders only expand into the
	// path, the query and the fragment of it.
	//nolint:gosec // G710: the target is built from the config, not from the request.
	http.Redirect(w, r, to.String(), rule.Status)

	return nil
}

// parseCaddyfile sets up the handler from the redirects_file directive.
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
)
