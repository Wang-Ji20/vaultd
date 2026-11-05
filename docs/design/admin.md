# Design: the admin portal

A web page at `http://127.0.0.1:8765/admin/`, served by the same program. It needs no
build step: Go templates, one hand-written stylesheet, and htmx.

## How a change flows

```
toggle clicked --> htmx posts the form --> check session and CSRF token
    --> validate --> back up old config --> save --> hot reload
    --> send the page back --> htmx swaps in the new <main>
```

If validation fails, the same page comes back with the message inline, and nothing has
been saved. The status stays 200, because htmx ignores the content of error responses.

Each handler returns a whole page. htmx picks out `#main`. So the forms also work with
JavaScript off: `<noscript>` shows a Save button, and the browser renders the response.

## Login and sessions

| Rule | Why |
|------|-----|
| No password set: only a setup page | There is nothing safe to show before one exists |
| bcrypt | Passwords, unlike tokens, can be guessed, so hashing must be slow |
| 5 attempts per minute, for everyone | One user on loopback: no one else to lock out |
| Cookie `HttpOnly`, `SameSite=Strict`, `Path=/admin` | Scripts cannot read it, other sites cannot send it, `/mcp` never gets it |
| Sessions in memory | A restart logs out, which is fine |
| A changed password ends old sessions | Changing it should remove any intruder |

The cookie has no `Secure` flag because the portal is plain HTTP on loopback, where
traffic never leaves the machine.

## CSRF

CSRF (cross-site request forgery) is a site making your browser post to the portal. Every
change needs a token tied to the session, sent as a form field or the `X-CSRF-Token`
header. The login form has no session yet, so its token sits in both a cookie and the
form. A foreign site can post a form but cannot read that cookie.

## Keeping the two doors apart

`/admin` looks only at the session cookie, and `/mcp` looks only at the bearer token. An
app token opens nothing in the portal, and a browser session opens nothing on `/mcp`.

## Content Security Policy

`default-src 'self'` bans inline scripts and styles, and a test checks the templates for
them. This shapes some choices:

- htmx runs with `allowEval: false` and without its injected indicator styles.
- No `hx-on` attributes, since they need eval. The copy buttons live in `app.js`.
- Progress uses `<progress>`, since a width would need an inline style.

`defaultSettleDelay: 0` removes a 20 ms window after each swap in which htmx has not yet
attached its listeners. A toggle pressed in that window flipped without being saved.

## Colours and contrast

The spec gives Apple's colours and also asks for WCAG AA. They conflict in light mode:

```
text on background         ratio   AA needs
secondary #3C3C4399        3.3     4.5
accent    #007AFF          3.6     4.5
danger    #FF3B30          3.6     4.5
white on accent #007AFF    4.0     4.5
```

So the spec's colours all stay, for fills and marks (toggles, dots, focus rings), which
need only 3:1. Text uses darker tokens such as `--accent-text`. `design_test.go`
computes every ratio from the stylesheet, so a later colour change cannot quietly fail AA.

Toggles use blue rather than iOS green, because `#34C759` on white is 2.2:1, under the 3:1
that controls need.

## Third-party files

`web/htmx.min.js` (0BSD) and `web/inter.woff2` (OFL, Latin subset) are the only vendored
files, as the maintainer decided. `go:embed` needs them at build time, so they cannot live
in the gitignored `external-deps/`. Their licences sit next to them. SF Pro is not bundled:
its licence limits it to Apple platforms. Apple devices still get it from the font stack.

Text outside the Latin subset falls back to system fonts.

## Tools shown for an upstream

Opening the Upstreams page connects to every upstream, for up to 3 seconds, so that its
tools can be listed before any is allowed. Locked tools have no toggle at all, and a forged
request for one is refused by config validation.

## Config snippets

Claude Desktop's snippet uses stdio, since its config file starts local commands. The
others use the shared address. The DeepSeek Harness snippet follows the fields in the spec
and has not been tried against that app.
