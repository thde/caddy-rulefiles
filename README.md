# Caddy handlers for `_headers` and `_redirects`

Two Caddy HTTP handler modules that serve the `_headers` and `_redirects` files
of a static site build, the formats popularised by Netlify and Cloudflare Pages.
They let the redirects and response headers of docs.nine.ch live next to the
site content instead of in the Caddyfile.

| Package         | Directive        | Module ID                      |
| --------------- | ---------------- | ------------------------------ |
| `headersfile`   | `headers_file`   | `http.handlers.headers_file`   |
| `redirectsfile` | `redirects_file` | `http.handlers.redirects_file` |

The files themselves are parsed by
[thde.io/rulefiles](https://pkg.go.dev/thde.io/rulefiles), which has no
dependency on Caddy. This module only registers the handlers and wires them into
the request lifecycle.

## Usage

Build a Caddy binary with both handlers:

```bash
xcaddy build \
  --with thde.io/caddy-rulefiles/headersfile \
  --with thde.io/caddy-rulefiles/redirectsfile
```

Then point the directives at the files of the build:

```caddyfile
:8080 {
	root * /srv

	headers_file * /srv/_headers
	redirects_file * /srv/_redirects

	try_files {path} /index.html
	file_server
}
```

Both directives take `[<matcher>] <file...>`. A leading argument starting with
`/` is taken for a path matcher, so the catch-all matcher `*` has to be spelled
out. Multiple files are loaded in order of precedence. Files are read once while
provisioning; changing them requires a config reload.

The directives are ordered after `header` and `redir` respectively, so rules are
matched against the path as it was requested, before `try_files` or a rewrite
changes it.

## File formats

`_redirects` holds one `<source> <target> [<status>]` rule per line, first match
wins. `_headers` holds an unindented path pattern followed by indented
`<name>: <value>` or `! <name>` lines, and all matching rules apply.

```
/old/path        /new/path
/docs/:id/*      /articles/:id/:splat  302
/gone            /                     410
```

```
/*
  X-Robots-Tag: noindex
/static/*
  Cache-Control: public, max-age=31536000, immutable
  ! X-Robots-Tag
```

See the [rulefiles documentation](https://pkg.go.dev/thde.io/rulefiles) for
the full syntax and how it differs from Netlify. Parse errors of all lines are
reported together and fail provisioning.

### Caddy specifics

- A redirect rule always takes precedence over an existing file. Netlify needs a
  trailing `!` for that; the marker is accepted and ignored. To give files
  precedence, use a matcher: `@missing not file` plus
  `redirects_file @missing /srv/_redirects`.
- A rewrite (`200`) hands the rewritten path to the next handler, so `try_files`
  and `file_server` serve it as if it had been requested.
- A status of `400` and above is returned as a Caddy error, which the
  `handle_errors` routes can answer.

## Tests

```bash
go test ./...
```
