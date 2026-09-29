// rela-server runs the data entry web application as a standalone HTTP server.
//
// Usage:
//
//	rela-server [-project .] [-port 8080] [-bind 127.0.0.1] [-allowed-origin URL]...
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/cmdexec"
	"github.com/Sourcehaven-BV/rela/internal/dataentry"
	"github.com/Sourcehaven-BV/rela/internal/jwtauth"
	"github.com/Sourcehaven-BV/rela/internal/scheduler"
	"github.com/Sourcehaven-BV/rela/internal/script"
)

// stringSliceFlag collects repeated -allowed-origin values.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

// serverFlags collects every command-line flag for rela-server.
// Extracting this lets main() stay under the funlen budget while
// keeping flag definitions in one readable block.
type serverFlags struct {
	projectDir        string
	port              string
	bind              string
	allowedOrigins    stringSliceFlag
	verbose           bool
	quiet             bool
	debugPprof        string
	principalHeader   string
	readOnly          bool
	unconfinedCommand bool
	// remoteMCP serves the MCP endpoint over HTTP at dataentry.MCPPath.
	// Off by default; requires the JWT identity flags below (dataentry
	// refuses to enable it otherwise — the endpoint is CSRF-exempt, and
	// that is only sound while rela verifies a bearer token itself).
	remoteMCP bool
	// allowUnauthCommands opts a network bind back into ungated command
	// execution when there is no acl.yaml. Off by default: a non-loopback bind
	// with no policy refuses commands. See docs/server-security.md.
	allowUnauthCommands bool
	// JWT identity: verify a signed-JWT assertion from an OIDC proxy against its
	// JWKS and stamp the verified subject as the principal. Provider-agnostic
	// (Pratique, oauth2-proxy, Pomerium, ...). All three of issuer/audience/jwks
	// must be set to enable; empty ⇒ disabled.
	jwtIssuer   string
	jwtAudience string
	jwtJWKSURL  string
	jwtHeader   string
	// Inbound-IdP webhook: verify a signed-JWT webhook body (same JWKS/issuer as
	// the identity JWT, but its OWN audience) and dispatch to a named action that
	// provisions a person entity. Both must be set to enable; empty ⇒ disabled.
	// Requires the JWT identity flags above (the webhook reuses that JWKS).
	webhookAudience string
	webhookAction   string
}

// coverage-ignore-func: flag wiring — exercised at startup, not in tests
// coverage-ignore-start: main-or-wiring: flag/DI startup wiring using the global flag.CommandLine and process env,
// exercised only at process
// start
func parseFlags() *serverFlags {
	f := &serverFlags{}
	flag.StringVar(&f.projectDir, "project", ".", "Path to the rela project directory")
	flag.StringVar(&f.port, "port", "8080", "HTTP port to listen on")
	flag.StringVar(&f.bind, "bind", "127.0.0.1",
		"Network interface to bind to. Defaults to loopback. Use 0.0.0.0 to expose on the LAN (see docs/server-security.md).")
	flag.Var(&f.allowedOrigins, "allowed-origin",
		"Extra origin permitted to call the API (repeatable). Used for dev servers like Vite on http://localhost:5173.")
	flag.BoolVar(&f.verbose, "verbose", false, "Verbose (debug) logging")
	flag.BoolVar(&f.quiet, "quiet", false, "Quiet (warn-only) logging")
	flag.StringVar(&f.debugPprof, "debug-pprof", "",
		"If set, serve net/http/pprof on this loopback address (e.g. 127.0.0.1:6060). "+
			"Diagnostic only. Refuses to bind to non-loopback addresses.")
	flag.StringVar(&f.principalHeader, "principal-header", "",
		"HTTP header to read for audit Principal.User (e.g. X-Forwarded-User). "+
			"Default empty: do not read any header. Operators can override per-process via "+
			"$RELA_DATAENTRY_USER (wins over the header). "+
			"WARNING: the header is only as trustworthy as the upstream proxy that sets it. "+
			"See docs/server-security.md.")
	flag.BoolVar(&f.readOnly, "read-only", false,
		"Refuse all writes. Useful for demos, maintenance windows, "+
			"observe-only deployments, and post-incident forensic mode. "+
			"Also enabled by RELA_READ_ONLY=1.")
	flag.BoolVar(&f.unconfinedCommand, "unconfined-commands", os.Getenv("RELA_UNCONFINED_COMMANDS") == "1",
		"Run external scan/transform/export commands UNCONFINED (no sandbox). "+
			"Only for hosts that cannot sandbox (no bubblewrap / a kernel without "+
			"unprivileged user namespaces / a locked-down container) or that isolate "+
			"at another layer. Without this, such a host refuses to run those "+
			"commands. Accepts running third-party parsers on untrusted input "+
			"unconfined — see docs/transforms.md. Also enabled by "+
			"RELA_UNCONFINED_COMMANDS=1.")
	flag.BoolVar(&f.remoteMCP, "mcp", os.Getenv("RELA_MCP") == "1",
		"Serve the Model Context Protocol endpoint over HTTP at /api/v1/_mcp, "+
			"so AI assistants can reach a deployed rela the same way `rela mcp` "+
			"serves a local one. Off by default. REQUIRES the -jwt-* identity "+
			"flags: the endpoint must be CSRF-exempt (MCP clients send no Origin), "+
			"which is only sound while rela verifies a bearer token itself — "+
			"startup is refused otherwise. Every tool call runs as the requesting "+
			"principal and is ACL-gated like any other API request. Also enabled "+
			"by RELA_MCP=1.")
	flag.BoolVar(&f.allowUnauthCommands, "allow-unauthenticated-commands",
		os.Getenv("RELA_ALLOW_UNAUTHENTICATED_COMMANDS") == "1",
		"Allow configured commands to run WITHOUT access control on a non-loopback "+
			"bind that has no acl.yaml. Off by default: such a bind refuses command "+
			"execution, because a command runs an arbitrary shell script for any "+
			"reachable client. Only for a single-user deployment isolated at another "+
			"layer (Docker port-publishing, a host firewall, or a reverse proxy that "+
			"authenticates). Loopback binds and deployments with an acl.yaml never "+
			"need this. See docs/server-security.md. Also enabled by "+
			"RELA_ALLOW_UNAUTHENTICATED_COMMANDS=1.")
	// JWT identity flags (env fallbacks $RELA_JWT_*). Verifying a SIGNED assertion
	// is safer than --principal-header (which merely trusts the proxy set a header).
	flag.StringVar(&f.jwtIssuer, "jwt-issuer", os.Getenv("RELA_JWT_ISSUER"),
		"Expected issuer (iss) of the identity JWT. Set with -jwt-audience and -jwt-jwks-url to "+
			"enable cryptographic principal verification.")
	flag.StringVar(&f.jwtAudience, "jwt-audience", os.Getenv("RELA_JWT_AUDIENCE"),
		"Expected audience (aud) of the identity JWT — this server's id, per the proxy config.")
	flag.StringVar(&f.jwtJWKSURL, "jwt-jwks-url", os.Getenv("RELA_JWT_JWKS_URL"),
		"HTTPS URL of the proxy's JWKS, used to verify the identity JWT's ES256 signature.")
	flag.StringVar(&f.jwtHeader, "jwt-header", envOr("RELA_JWT_HEADER", "X-Auth-Assertion"),
		"Request header carrying the signed identity JWT (a leading 'Bearer ' is stripped). "+
			"Point it at whatever your proxy injects, e.g. X-Pratique-Assertion or Authorization.")
	// Inbound-IdP webhook flags (env fallbacks $RELA_WEBHOOK_*). Enable POST
	// /webhooks/idp: a signed-JWT callback that provisions a user via an action.
	flag.StringVar(&f.webhookAudience, "webhook-audience", os.Getenv("RELA_WEBHOOK_AUDIENCE"),
		"Expected audience (aud) of an inbound IdP webhook JWT — distinct from -jwt-audience so an "+
			"identity assertion can't be replayed as a webhook. Set with -webhook-action to enable "+
			"POST /webhooks/idp. Reuses the -jwt-issuer/-jwt-jwks-url trust root.")
	flag.StringVar(&f.webhookAction, "webhook-action", envOr("RELA_WEBHOOK_ACTION", ""),
		"Name of the action a verified IdP webhook dispatches to (e.g. idp-sync). The action "+
			"receives event/user_id/org_id as params and provisions the user.")
	// Note: there is no --database-url flag, and must not be. The postgres
	// build takes the DSN from $RELA_DATABASE_URL (or, for an embedding
	// caller, appbuild.WithDatabaseURL) — never from argv, so the credential
	// cannot land in process listings or shell history.
	// See appbuild.Config.DatabaseURL.
	flag.Parse()
	if os.Getenv("RELA_READ_ONLY") == "1" {
		f.readOnly = true
	}
	return f
}

// coverage-ignore-end

// discoverOptions maps server flags to appbuild options. --read-only injects a
// read-only ACL. The postgres DSN is not an option here — it is read from
// $RELA_DATABASE_URL by appbuild.Discover (env-only, never a flag).
func discoverOptions(f *serverFlags) []appbuild.Option {
	var opts []appbuild.Option
	if f.readOnly {
		opts = append(opts, appbuild.WithACL(acl.ReadOnlyACL{}))
	}
	return opts
}

// discoverProject resolves the project dir and builds the services, exiting on
// any failure (a daemon has nothing to fall back to). It warns when read-only.
func discoverProject(f *serverFlags) *appbuild.Services {
	absDir, err := filepath.Abs(f.projectDir)
	if err != nil {
		slog.Error("invalid project dir", "error", err)
		os.Exit(1)
	}
	svc, err := appbuild.Discover(absDir, script.NewEngine(), discoverOptions(f)...)
	if err != nil {
		slog.Error("failed to initialize project services", "error", err)
		os.Exit(1)
	}
	if f.readOnly {
		slog.Warn("rela-server is read-only; every write request will be refused")
	}
	return svc
}

// applyCommandConfinement records the host-level command-confinement decision
// before any runner is built, warning once (like a disabled attachment scan)
// when commands will run unconfined.
func applyCommandConfinement(unconfined bool) {
	if cmdexec.SetUnconfinedByDefault(unconfined) {
		slog.Warn("external commands run UNCONFINED (--unconfined-commands / " +
			"RELA_UNCONFINED_COMMANDS=1): scan/transform/export run third-party " +
			"parsers on untrusted input with no sandbox. Ensure isolation is " +
			"provided at another layer.")
	}
}

// envOr returns $key, or def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// identityMode is the identity source the server runs with. The two modes are
// mutually exclusive by construction — see [validateIdentityFlags].
type identityMode int

const (
	// identityHeader is the legacy chain: $RELA_DATAENTRY_USER, then the
	// proxy-trusted --principal-header, then "unknown". Fails OPEN by design
	// (an absent header yields "unknown", not a denial) and is unchanged.
	identityHeader identityMode = iota
	// identityJWT is fail-closed verified-JWT identity. Every API request must
	// carry an assertion that verifies, or it is denied.
	identityJWT
)

// validateIdentityFlags classifies the configured identity sources, returning an
// error when they conflict.
//
// **Why JWT identity is exclusive.** Layering a verified JWT over a plain header
// in one resolver chain means a JWT verification failure falls THROUGH to the
// spoofable header. Anyone able to disrupt JWKS reachability — network egress,
// DNS, an IdP outage — thereby converts the server from verified identity to
// trusted-header identity, and it keeps serving as if nothing changed. That is an
// attacker-triggerable auth downgrade, so the combination is refused at startup
// rather than warned about: the downgrade happens per-request, long after anyone
// is reading startup logs. The same reasoning applies to $RELA_DATAENTRY_USER,
// which would override a cryptographically proven subject with an env var.
//
// envUser is passed in rather than read from the environment so this stays a pure
// function — the caller owns both the lookup and the exit.
func validateIdentityFlags(f *serverFlags, envUser string) (identityMode, error) {
	set := 0
	for _, v := range []string{f.jwtIssuer, f.jwtAudience, f.jwtJWKSURL} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
		return identityHeader, nil
	case 3: // fully configured — checked against the other sources below
	default:
		// A partially-configured JWT used to silently disable identity, leaving a
		// server the operator believes is authenticating when it is not.
		return identityHeader, errors.New("jwt identity requires all of " +
			"-jwt-issuer, -jwt-audience and -jwt-jwks-url (or none)")
	}

	if f.principalHeader != "" {
		return identityHeader, errors.New("-jwt-* and -principal-header are mutually " +
			"exclusive: a JWT verification failure would fall through to the " +
			"spoofable header, downgrading verified identity. Choose one. " +
			"See docs/server-security.md")
	}
	if envUser != "" {
		return identityHeader, fmt.Errorf("$%s cannot be set with -jwt-*: it would "+
			"override the cryptographically verified subject. Unset it, or run "+
			"without JWT identity. See docs/server-security.md",
			dataentry.EnvDataEntryUserVar)
	}
	if f.jwtHeader == "" {
		// An empty header name reads every assertion as absent, so the server
		// would boot clean and then deny every API request. Fail-closed, but
		// silently — catch it here where the cause is obvious.
		return identityHeader, errors.New("-jwt-header must not be empty when jwt identity is enabled")
	}
	return identityJWT, nil
}

// wirePrincipalResolvers installs the identity source on the app, per mode.
//
// identityHeader keeps the legacy chain: $RELA_DATAENTRY_USER, then the plain
// header, then "unknown". identityJWT installs the fail-closed gate INSTEAD of a
// resolver chain — the JWT resolver is deliberately not chained, because with a
// single exclusive source there is nothing to fall through to.
//
// coverage-ignore-func: startup wiring — the decision it acts on is validateIdentityFlags.
// coverage-ignore-start: main-or-wiring: startup wiring that installs the identity source on the app and os.Exits on
// SetJWTGate failure; the
// pure decision it acts on (validateIdentityFlags) is tested separately
func wirePrincipalResolvers(app *dataentry.App, f *serverFlags, idv *jwtauth.Verifier, mode identityMode) {
	if mode == identityJWT {
		if idv == nil {
			// Unreachable: identityJWT implies all three JWT flags are set, so
			// buildIdentityVerifier either returned a verifier or exited. Checked
			// anyway because the alternative — storing a nil verifier behind a
			// non-nil interface — panics on the first request instead of here.
			slog.Error("jwt identity selected but no verifier was built")
			os.Exit(1)
		}
		if err := app.SetJWTGate(dataentry.JWTGateConfig{
			Verifier:   assertionVerifierAdapter{idv},
			HeaderName: f.jwtHeader,
			// Injected as a predicate so dataentry needn't import jwtauth.
			KeysUnavailable: func(err error) bool {
				return errors.Is(err, jwtauth.ErrKeysUnavailable)
			},
		}); err != nil {
			slog.Error("failed to enable jwt identity", "error", err)
			os.Exit(1)
		}
		// Vary on the assertion header: under ACL, API responses are
		// per-principal (TKT-VMD8), and the assertion determines the principal.
		app.SetPrincipalHeader(f.jwtHeader)
		return
	}

	app.SetPrincipalResolver(dataentry.ChainResolvers(
		dataentry.EnvPrincipalResolver(),
		dataentry.HeaderPrincipalResolver(f.principalHeader),
	))
	app.SetPrincipalHeader(f.principalHeader)
}

// coverage-ignore-end

// buildIdentityVerifier builds the shared signed-JWT verifier from the flags, or
// returns nil when JWT identity is disabled (any of issuer/audience/jwks unset). A
// build failure — a bad config, a non-https JWKS URL, or an unreachable JWKS — is
// fatal so identity never silently no-ops (jwtauth.New fetches the JWKS up front
// and errors if it can't). The one verifier is reused by both the principal
// resolver and the webhook receiver, so the JWKS is fetched once.
//
// coverage-ignore-func: startup wiring — exercised via jwtauth's own tests.
// coverage-ignore-start: main-or-wiring: startup wiring; jwtauth.New fetches a real JWKS over HTTPS at boot and this
// os.Exits on failure —
// verifier construction is covered by jwtauth's own tests
func buildIdentityVerifier(ctx context.Context, f *serverFlags) *jwtauth.Verifier {
	if f.jwtIssuer == "" || f.jwtAudience == "" || f.jwtJWKSURL == "" {
		return nil
	}
	v, err := jwtauth.New(ctx, jwtauth.Config{
		Issuer:   f.jwtIssuer,
		Audience: f.jwtAudience,
		JWKSURL:  f.jwtJWKSURL,
	})
	if err != nil {
		slog.Error("jwt identity: failed to initialize verifier", "error", err)
		os.Exit(1)
	}
	slog.Info("jwt identity enabled", "issuer", f.jwtIssuer, "header", f.jwtHeader)
	return v
}

// coverage-ignore-end

// wireWebhookReceiver enables POST /webhooks/idp when -webhook-audience and
// -webhook-action are both set. It requires JWT identity (the webhook reuses that
// verifier's JWKS/issuer); a webhook audience without JWT identity, or a build
// failure, is fatal so a misconfiguration fails loud rather than silently leaving
// the endpoint off.
//
// coverage-ignore-func: startup wiring — exercised via the shim + verifier tests.
// coverage-ignore-start: main-or-wiring: startup wiring; every guard branch terminates in os.Exit and the enable path
// installs a real webhook
// verifier on the app, reachable only at process start
func wireWebhookReceiver(app *dataentry.App, f *serverFlags, idv *jwtauth.Verifier) {
	if f.webhookAudience == "" && f.webhookAction == "" {
		return // disabled
	}
	if f.webhookAudience == "" || f.webhookAction == "" {
		slog.Error("webhook: both -webhook-audience and -webhook-action are required to enable POST /webhooks/idp")
		os.Exit(1)
	}
	if idv == nil {
		slog.Error("webhook: -webhook-* requires JWT identity (-jwt-issuer/-jwt-audience/-jwt-jwks-url); the webhook reuses that JWKS")
		os.Exit(1)
	}
	wv, err := jwtauth.NewWebhookVerifier(idv, f.webhookAudience)
	if err != nil {
		slog.Error("webhook: failed to initialize verifier", "error", err)
		os.Exit(1)
	}
	app.SetWebhookReceiver(webhookVerifierAdapter{wv}, f.webhookAction)
	slog.Info("idp webhook enabled", "audience", f.webhookAudience, "action", f.webhookAction)
}

// assertionVerifierAdapter bridges the concrete jwtauth.Verifier to the
// dataentry JWT gate's expected shape, translating jwtauth.AssertionClaims into
// dataentry.AssertedIdentity. Like webhookVerifierAdapter it lives in the wiring
// layer — the one place allowed to import both packages — so dataentry needn't
// depend on jwtauth (the inward-pointing layering rule).
//
// The gate consumes VerifyAssertion (not VerifySubject) so the assertion's
// org/roles reach the Principal it stamps; a subject-only adapter here would
// silently strip every asserted role (TKT-OJL2GN).
//
// The same applies to principal_type/scope (TKT-IAC8TX), with the failure
// running the other way: dropping a role removes access, dropping a
// principal_type removes the CEILING and hands a restricted client its acting
// user's full grants. Every claim this adapter forgets is a silent widening.
type assertionVerifierAdapter struct{ v *jwtauth.Verifier }

func (a assertionVerifierAdapter) VerifyAssertion(
	ctx context.Context, raw string,
) (dataentry.AssertedIdentity, error) {
	c, err := a.v.VerifyAssertion(ctx, raw)
	if err != nil {
		return dataentry.AssertedIdentity{}, err
	}
	return dataentry.AssertedIdentity{
		Subject:       c.Subject,
		OrgID:         c.OrgID,
		OrgSlug:       c.OrgSlug,
		Roles:         c.Roles,
		PrincipalType: c.PrincipalType,
		Scopes:        c.Scopes,
		Email:         c.Email,
	}, nil
}

// coverage-ignore-end

// webhookVerifierAdapter bridges the concrete jwtauth.WebhookVerifier to the
// dataentry receiver's expected shape, translating jwtauth.WebhookClaims into
// dataentry.WebhookClaims. This adapter lives in the wiring layer — the one place
// allowed to import both packages — so dataentry needn't depend on jwtauth (the
// inward-pointing layering rule, mirroring the JWTPrincipalResolver seam).
type webhookVerifierAdapter struct{ v *jwtauth.WebhookVerifier }

func (a webhookVerifierAdapter) VerifyWebhook(ctx context.Context, raw string) (dataentry.WebhookClaims, error) {
	c, err := a.v.VerifyWebhook(ctx, raw)
	if err != nil {
		return dataentry.WebhookClaims{}, err
	}
	return dataentry.WebhookClaims{Event: c.Event, UserID: c.UserID, OrgID: c.OrgID, ID: c.ID}, nil
}

// coverage-ignore-func: startup wiring — exercised at startup, not in tests
//
// wireWorlds gives the app request-level world selection and the link
// resolution that must accompany it.
//
// The two are wired TOGETHER and never separately: a surface that can SELECT a
// world but cannot resolve that world's links renders every page with no
// relations, which reads as a data problem rather than a wiring gap. Keeping
// them in one function makes the pairing structural instead of a convention
// someone has to notice.
//
// Without either, the app serves the default world only and refuses any other
// `?world=` — the right posture for a surface whose wiring never opted in.
func wireWorlds(app *dataentry.App, svc *appbuild.Services) {
	app.SetWorlds(appbuild.CompiledWorlds(svc))
	if err := dataentry.SetWorldNeighbors(app, svc.Store(), appbuild.RelationScopes(svc)); err != nil {
		slog.Error("failed to wire world-scoped relations", "error", err)
		os.Exit(1)
	}
}

// coverage-ignore-func: main function - entry point
// coverage-ignore-start: main-or-wiring: process entry point — discovers services, builds the app, binds a listener,
// and calls ListenAndServe;
// every branch is startup wiring or os.Exit
func main() {
	f := parseFlags()

	configureLogging(f.verbose, f.quiet)

	if err := dataentry.CheckEmbeddedSPA(); err != nil {
		slog.Error("embedded SPA check failed", "error", err)
		os.Exit(1)
	}

	// Record the command-confinement decision BEFORE building any services: the
	// choice is read by every cmdexec.Runner at construction, and appbuild.Discover
	// may build one. Applying it after discovery would confine (or fail closed on)
	// a host the operator explicitly opted out of. Keep this above discoverProject.
	applyCommandConfinement(f.unconfinedCommand)

	// No svc.Close(): rela-server is a daemon — it runs until the process exits,
	// at which point the OS reclaims file descriptors and goroutines. Per-project
	// Close() *is* required in long-running hosts that switch projects (see
	// rela-desktop); this is the daemon-lifetime case.
	svc := discoverProject(f)

	fieldResolver := buildFieldResolver(svc)

	commandAuthz := buildCommandAuthorizer(f, svc)

	app, err := dataentry.NewApp(
		svc.FS(), svc.Paths(), svc.Meta(), svc.Store(), svc.Versions(),
		svc.EntityManager(), svc.Searcher(), svc.VisibleSearcher(), svc.ACL(),
		fieldResolver,
		svc.Audit(),
		svc.State(),
		commandAuthz,
	)
	if err != nil {
		var configErr *dataentry.ConfigValidationError
		if errors.As(err, &configErr) {
			fmt.Fprintln(os.Stderr, "Configuration validation failed:")
			for _, e := range configErr.Errors {
				fmt.Fprintf(os.Stderr, "  - %s\n", e)
			}
			os.Exit(1)
		}
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}

	// CalDAV needs the alias service to remember client-created resources;
	// without it the routes are not registered at all.
	app.SetCalDAVAliases(svc.CalDAVAliases())
	app.SetComments(svc.Comments())
	dataentry.SetTwins(app, appbuild.Twins(svc))

	wireWorlds(app, svc)

	// Next-action per-user state. The composition root picks the backend
	// (durable over state.KV, or the store-native one on postgres); this only
	// hands the app what it built.
	if err := app.SetUserState(svc.UserState()); err != nil {
		slog.Error("failed to wire next-action state", "error", err)
		os.Exit(1)
	}

	wireConditionCompilers(app)

	// Start file watcher for live-reload.
	// The watcher goroutine is cleaned up on process exit.
	if err := app.StartWatching(); err != nil {
		slog.Warn("file watcher not started", "error", err)
	} else {
		slog.Info("file watcher started for live-reload")
	}

	addr := net.JoinHostPort(f.bind, f.port)
	if err := app.SetSecurityConfig(dataentry.SecurityConfig{
		BindAddress:    addr,
		AllowedOrigins: f.allowedOrigins,
	}); err != nil {
		slog.Error("invalid security configuration", "error", err)
		os.Exit(1)
	}

	// Identity sources are mutually exclusive — validate BEFORE building anything,
	// so a conflicting config never reaches a running server.
	wireIdentityAndMCP(app, svc, f)

	srv := newHTTPServer(addr, app.NewRouter())

	if !isLoopbackHost(f.bind) {
		slog.Warn("rela-server bound beyond loopback; see docs/server-security.md for threat model",
			"bind", f.bind)
		if f.principalHeader != "" {
			// The combination — exposed bind + header-trusted principal —
			// is exactly the deployment the security doc warns against.
			// Log a second time so an operator scanning startup output
			// sees the explicit hazard, not just the generic bind warning.
			slog.Warn("--principal-header set on non-loopback bind: "+
				"audit attribution trusts an HTTP header from the network; "+
				"only safe if a reverse proxy strips + replaces the header. "+
				"See docs/server-security.md.",
				"bind", f.bind, "header", f.principalHeader)
		}
		if shouldWarnNoACL(svc.ACL(), f.readOnly) {
			// Non-loopback bind without `acl.yaml` and without
			// `--read-only` means anyone reaching this server can write.
			// The Origin / Host hardening from FEAT-ESLP still blocks
			// browser-driven cross-origin writes, but a direct API call
			// from inside the network is unauthenticated by design.
			// Operators serving multi-user need either `acl.yaml` or a
			// reverse proxy that enforces access; the warning surfaces
			// the gap at startup rather than at first-incident time.
			slog.Warn("rela-server bound beyond loopback without acl.yaml: "+
				"every reachable client can write. Add an acl.yaml at the project "+
				"root or pass --read-only. See docs/server-security.md.",
				"bind", f.bind)
		}
	}
	// Start background scheduler if schedules.yaml exists.
	// *appbuild.Services satisfies scheduler.WorkspaceProvider
	// structurally (Paths / Config / State / LuaWriteDeps).
	// The goroutine is cleaned up on process exit.
	scheduler.StartBackground(context.Background(), svc, slog.Default())

	if err := startPprofIfRequested(f.debugPprof); err != nil {
		slog.Error("pprof startup failed", "error", err)
		os.Exit(1)
	}

	slog.Info("starting server", "name", app.Cfg().App.Name, "addr", "http://"+addr)
	if err := serveUntilSignal(srv); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// shutdownGrace bounds how long a graceful shutdown waits for in-flight
// requests to finish before the process exits.
const shutdownGrace = 5 * time.Second

// serveUntilSignal runs srv until it fails or a SIGINT/SIGTERM arrives, then
// shuts it down gracefully. Graceful shutdown drains in-flight requests, and is
// REQUIRED for coverage-instrumented builds: Go's `-cover` runtime only writes
// counter data on a clean process exit, so a bare SIGTERM that killed the
// process mid-serve would discard all coverage gathered during e2e runs.
// Returning an error (rather than calling os.Exit here) keeps the deferred
// cancel reachable.
func serveUntilSignal(srv *http.Server) error {
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		return err
	case s := <-sig:
		slog.Info("shutting down", "signal", s.String())
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}

// coverage-ignore-end

// newHTTPServer serves the data-entry handler with cleartext HTTP/2
// alongside HTTP/1.1. Go's http.Server only negotiates HTTP/2 automatically
// when serving TLS, so for plaintext we opt in via Protocols.SetUnencryptedHTTP2.
// This matters because the data-entry SPA holds a permanent EventSource to
// /api/v1/_events — under HTTP/1.1 that eats one of the browser's per-host
// connection slots (Firefox default 6), and under concurrent navigation the
// pool runs dry. HTTP/2 multiplexes many streams over a single connection
// so the per-host limit becomes irrelevant. The opt-in is transparent to
// HTTP/1.1 clients (curl without --http2) and to all existing middlewares —
// they still see a normal *http.Request with Host/Origin/etc. populated the
// same way.
//
// coverage-ignore-func: server construction, exercised via integration tests
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout intentionally 0: SSE and command-exec stream
		// long-lived responses and would otherwise be killed mid-flight.
		// Trade-off: a slow-reading client can hold a goroutine open as
		// long as it accepts data slowly. On a loopback bind that risk
		// is limited to local processes; see docs/server-security.md for the
		// residual exposure when --bind opts into LAN access.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
}

// commandAuthorizer aliases the authorizer the dataentry package hands back.
// The interface itself is unexported there (it is a dataentry-internal seam),
// so this alias is how main names the value it threads into NewApp.
type commandAuthorizer = interface {
	Authorize(ctx context.Context, cmd dataentry.CommandConfig) bool
}

// buildCommandAuthorizer picks the command-exec authorizer once, from the
// configured ACL plus the bind and the explicit override. Exits rather than
// degrading: a server that cannot decide how to gate shell execution must not
// start and quietly pick a default.
func buildCommandAuthorizer(f *serverFlags, svc *appbuild.Services) commandAuthorizer {
	authz, err := dataentry.SelectCommandAuthorizer(
		svc.ACL(), svc.ACLDeclarative(), isLoopbackHost(f.bind), f.allowUnauthCommands,
		dataentry.CommandAuthNotifier{
			OnOverride: func() { warnUnauthenticatedCommands(f.bind) },
			OnRefuse:   func() { warnCommandsRefused(f.bind) },
		})
	if err != nil {
		slog.Error("failed to select command authorizer", "error", err)
		os.Exit(1)
	}
	return authz
}

// warnUnauthenticatedCommands is invoked by SelectCommandAuthorizer when the
// --allow-unauthenticated-commands override actually takes effect on a
// non-loopback bind, so the hazard shows up in every boot log.
//
// coverage-ignore: startup log side effect.
func warnUnauthenticatedCommands(bind string) {
	slog.Warn("commands run WITHOUT access control on this non-loopback bind: "+
		"--allow-unauthenticated-commands is set and there is no acl.yaml. "+
		"Any client reaching this server can execute configured shell commands. "+
		"See docs/server-security.md.",
		"bind", bind)
}

// warnCommandsRefused explains a refusal at startup instead of letting the
// operator discover it as vanished buttons and a bare 403 with nothing in the
// log. Symmetric with warnUnauthenticatedCommands: the override is loud because
// it opens a network shell, and the refusal is loud because it changes the
// behavior of a deployment that used to work.
//
// It fires whenever the refusing authorizer is selected, without consulting the
// command list: the authorizer is chosen before the config is loaded, and a
// project with no commands: entries is only told that a surface it does not use
// is closed.
func warnCommandsRefused(bind string) {
	slog.Warn("configured commands are REFUSED on this non-loopback bind: "+
		"there is no acl.yaml and --allow-unauthenticated-commands is not set. "+
		"Add an acl.yaml and give each command a permission:, or pass "+
		"--allow-unauthenticated-commands if this deployment is isolated another way. "+
		"See docs/server-security.md.",
		"bind", bind)
}

// buildFieldResolver constructs the data-entry affordance resolver
// from the active services. A predicate compile error in acl.yaml is
// fatal — surfaced loudly rather than silently disabling a gate.
// coverage-ignore-start: main-or-wiring: startup wiring taking the concrete *appbuild.Services and os.Exiting on a
// resolver-build failure;
// reachable only from main()
func buildFieldResolver(svc *appbuild.Services) dataentry.FieldVerdictResolver {
	resolver, err := dataentry.ResolverFromServices(svc)
	if err != nil {
		slog.Error("failed to build affordance resolver", "error", err)
		os.Exit(1)
	}
	return resolver
}

// coverage-ignore-end

// shouldWarnNoACL reports whether the operator should be told they
// are running with no access control on a non-loopback bind.
//
// True iff:
//   - The active ACL is [acl.NopACL] (i.e. no `acl.yaml`, and the
//     operator didn't pass `--read-only`).
//   - `--read-only` is NOT set (read-only is a stronger guarantee
//     than `acl.yaml` — no need to nag).
//
// Extracted so the warning logic is unit-testable without spinning
// up a server.
func shouldWarnNoACL(active acl.ACL, readOnly bool) bool {
	if readOnly {
		return false
	}
	_, isNop := active.(acl.NopACL)
	return isNop
}

// configureLogging sets the default slog logger based on verbose/quiet flags.
func configureLogging(verbose, quiet bool) {
	level := slog.LevelInfo
	switch {
	case verbose:
		level = slog.LevelDebug
	case quiet:
		level = slog.LevelWarn
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// startPprofIfRequested launches a diagnostic net/http/pprof listener on
// a separate loopback-only port if addr is non-empty. Returns nil and
// does nothing if addr is empty (the default; pprof off).
//
// pprof handlers are registered on a private mux rather than via the
// usual `_ "net/http/pprof"` blank import, which would register on
// http.DefaultServeMux and risk leaking the diagnostic surface if any
// other code in the process accidentally serves DefaultServeMux. The
// listener also refuses non-loopback binds so a misconfigured
// --bind 0.0.0.0 cannot accidentally expose goroutine dumps to the LAN.
//
// coverage-ignore-func: diagnostic-only, off by default
func startPprofIfRequested(addr string) error {
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --debug-pprof address %q: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("--debug-pprof must bind to loopback (got %q)", host)
	}

	// Build our own mux with pprof handlers explicitly registered. The
	// stdlib net/http/pprof package exposes the handler functions
	// directly so we can wire them onto a private mux.
	// coverage-ignore-start: os-fs-event: launches a real net/http/pprof listener in a goroutine (diagnostic-only, off
	// by default); the pure
	// validation branches above are reachable and left as gaps
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("pprof listening", "addr", "http://"+addr+"/debug/pprof/")
		if err := srv.ListenAndServe(); err != nil {
			slog.Warn("pprof server stopped", "error", err)
		}
	}()
	return nil
	// coverage-ignore-end
}

// isLoopbackHost reports whether host is the loopback interface.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// wireConditionCompilers supplies the predicate compilers backing a
// next-action source's `condition:`, a list's or kanban's `condition:`, and an
// entity type's `query_scopes:`.
//
// All three live above internal/dataentry (arch-lint keeps the condition
// engine there), so the composition root bridges them rather than dataentry
// importing one. Extracted from main() to keep it inside the funlen budget.
//
// Every failure is fatal: a silently absent compiler leaves conditions
// unevaluated and scopes unapplied, which shows rows the operator explicitly
// excluded — and nothing on screen would say so.
//
// The view halves need AdaptViewConditions / AdaptQueryScopes because appbuild
// cannot name dataentry's types (dataentry's tests import appbuild, closing a
// cycle), so each returns a structurally identical func under its own name.
// This call is where the two meet: a drift between them fails to compile here.
func wireConditionCompilers(app *dataentry.App) {
	if err := app.SetNextActionMatchers(appbuild.NextActionMatchers); err != nil {
		slog.Error("failed to wire next-action matchers", "error", err)
		os.Exit(1)
	}
	if err := app.SetViewConditions(
		dataentry.AdaptViewConditions(appbuild.ViewConditions),
	); err != nil {
		slog.Error("failed to wire view conditions", "error", err)
		os.Exit(1)
	}
	if err := app.SetQueryScopeResolver(
		dataentry.AdaptQueryScopes(appbuild.QueryScopes),
	); err != nil {
		slog.Error("failed to wire query scopes", "error", err)
		os.Exit(1)
	}
}
