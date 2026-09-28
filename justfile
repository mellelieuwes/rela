# rela project justfile

# Variables
build_dir := "bin"
# Keep these in sync with .github/workflows/ci.yml (lint and arch-lint jobs).
golangci_lint_version := "v2.11.4"
go_arch_lint_version := "v1.15.0"
go_packages := "$(go list ./... | grep -v /frontend/node_modules/)"

# Default recipe
default: lint test build

# ── Build ──

# Build the CLI binary
build-cli:
    @echo "Building rela CLI..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o {{build_dir}}/rela ./cmd/rela

# Build the data entry server (includes Vue frontend)
build-server: build-frontend
    @echo "Building rela-server..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-server ./cmd/rela-server

# Build rela-server embedding the E2E (development-mode) frontend, so
# DEV-guarded test hooks are available to the E2E suite (issue #890).
build-server-e2e: build-frontend-e2e
    @echo "Building rela-server (E2E frontend)..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-server ./cmd/rela-server

# Build the desktop app
build-desktop: build-frontend
    @echo "Building rela-desktop..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags production -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-desktop ./cmd/rela-desktop

# Build and install the desktop app into /Applications
[macos]
install-desktop: build-desktop
    #!/usr/bin/env bash
    # Assembles the same .app bundle the release workflow builds — binary +
    # .icns + Info.plist, ad-hoc signed — then registers it with Launch
    # Services so .rela bundles open with it. Ad-hoc signing (-s -) matches
    # CI: enough to run locally, but NOT enough for native notifications,
    # which need a real Developer ID.
    set -euo pipefail
    APP="/Applications/Rela Desktop.app"
    if pgrep -f "Rela Desktop.app" >/dev/null 2>&1; then
        echo "Quitting the running Rela Desktop..."
        osascript -e 'quit app "Rela Desktop"' 2>/dev/null || killall rela-desktop 2>/dev/null || true
        sleep 2
    fi
    echo "Installing to $APP..."
    rm -rf "$APP"
    mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
    cp {{build_dir}}/rela-desktop "$APP/Contents/MacOS/rela-desktop"
    cp build/package/macos/rela-desktop.icns "$APP/Contents/Resources/"
    sed "s/VERSION_PLACEHOLDER/dev/g" build/package/macos/Info.plist > "$APP/Contents/Info.plist"
    codesign --force --deep -s - "$APP"
    /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP"
    touch "$APP"
    echo "Installed. Open from /Applications, or: open -a 'Rela Desktop'"

# Build and install the desktop app into LOCALAPPDATA
[windows]
install-desktop: build-frontend
    #!/usr/bin/env bash
    # Windows needs no CGO (Wails v3 defaults CGO_ENABLED=0 there) and has no
    # bundle format, so this does not reuse build-desktop's macOS-only flags.
    set -euo pipefail
    DEST="${LOCALAPPDATA:-$HOME/AppData/Local}/Rela Desktop"
    echo "Installing to $DEST..."
    mkdir -p "$DEST"
    CGO_ENABLED=0 go build -tags production -trimpath -ldflags "-s -w" \
        -o "$DEST/rela-desktop.exe" ./cmd/rela-desktop
    echo "Installed to $DEST/rela-desktop.exe"
    echo "File associations need the MSI (build/package/windows/rela-desktop.wxs)."

# Desktop builds target macOS and Windows only
[linux]
install-desktop:
    @echo "Linux is not a desktop target — see .github/workflows/release.yml." && exit 1

# Build the desktop app with debug/devtools support for E2E testing
build-desktop-debug: build-frontend
    @echo "Building rela-desktop (debug)..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -o {{build_dir}}/rela-desktop ./cmd/rela-desktop

# Build the PostgreSQL-backed CLI binary (rela-postgres)
build-cli-postgres:
    @echo "Building rela-postgres CLI..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -tags postgres -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-postgres ./cmd/rela

# Build the PostgreSQL-backed data entry server (rela-server-postgres)
build-server-postgres: build-frontend
    @echo "Building rela-server-postgres..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -tags postgres -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-server-postgres ./cmd/rela-server

# Build the SQLite-backed CLI binary (rela-sqlite)
build-cli-sqlite:
    @echo "Building rela-sqlite CLI..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -tags sqlite -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-sqlite ./cmd/rela

# Build the SQLite-backed data entry server (rela-server-sqlite)
build-server-sqlite: build-frontend
    @echo "Building rela-server-sqlite..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -tags sqlite -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-server-sqlite ./cmd/rela-server

# Build the docs CLI (rela-docs). Embeds the Vue frontend: screenshot{}
# islands drive the data-entry SPA in a headless browser. This is the only
# binary that links chromedp — kept out of rela / rela-server on purpose.
build-docs: build-frontend
    @echo "Building rela-docs..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-docs ./cmd/rela-docs

# Build the docs CLI against the POSTGRES backend (rela-docs-postgres).
#
# Same binary as build-docs, one build tag different — which is the whole
# point: the storage backend is chosen at compile time, so a manual that wants
# to photograph a postgres-only capability (version history, whose
# HistoryReader only pgstore implements) has to be built here. On the default
# build that page can only ever show "not available for this deployment".
#
# Its screenshot{}/api{} temp project is pinned to a PRIVATE, randomly-named
# scratch schema that is dropped at teardown, so a docs build never writes the
# manual's fixture into the operator's real data.
build-docs-postgres: build-frontend
    @echo "Building rela-docs-postgres..."
    @mkdir -p {{build_dir}}
    CGO_ENABLED=0 go build -tags postgres -trimpath -ldflags "-s -w" -o {{build_dir}}/rela-docs-postgres ./cmd/rela-docs

# Build all binaries
build: build-cli build-server build-docs build-desktop

# Build the postgres-tagged binaries (FS binaries unaffected)
build-postgres: build-cli-postgres build-server-postgres

# Build the sqlite-tagged binaries (FS binaries unaffected)
build-sqlite: build-cli-sqlite build-server-sqlite

# Install CLI to ~/bin
#
# Installs both backend variants: the default FS build (`rela`,
# `rela-server`) and the postgres build (`rela-postgres`,
# `rela-server-postgres`). The two are separate binaries because the backend
# is chosen at compile time by build tag, and a project on postgres reaches
# for the `-postgres` names — so installing only the FS pair leaves those
# stale, and a fix appears not to land.
install: build-cli build-server build-cli-postgres build-server-postgres
    @echo "Installing rela, rela-server and their postgres variants to ~/bin..."
    @mkdir -p ~/bin
    @install {{build_dir}}/rela ~/bin/rela
    @install {{build_dir}}/rela-server ~/bin/rela-server
    @install {{build_dir}}/rela-postgres ~/bin/rela-postgres
    @install {{build_dir}}/rela-server-postgres ~/bin/rela-server-postgres
    @echo "Done! Make sure ~/bin is in your PATH."

# Clean build artifacts
clean:
    @echo "Cleaning..."
    rm -rf {{build_dir}}
    go clean -cache -testcache

# ── Test ──

# Run tests with race detection. -shuffle=on randomizes test order to
# surface inter-test ordering dependencies; on failure the seed is
# printed (reproduce with -shuffle=<seed>).
test:
    @echo "Running tests..."
    go test -race -cover -shuffle=on {{go_packages}}

# Run tests with verbose output
test-verbose:
    @echo "Running tests (verbose)..."
    go test -race -cover -shuffle=on -v {{go_packages}}

# Run the postgres-tagged tests against a real PostgreSQL.
# Requires RELA_TEST_DATABASE_URL, e.g.:
#   RELA_TEST_DATABASE_URL=postgres://user@127.0.0.1:5432/rela_test?sslmode=disable just test-postgres
# Without it, the pgstore conformance suite skips (so this stays a no-op-safe target).
#
# Set RELA_TEST_DATABASE_REQUIRED=1 to turn that skip into a hard failure —
# use it anywhere "pgstore is green" is treated as a gate rather than a
# convenience, since a skip and a pass look identical in the exit code. CI's
# Postgres Backend job sets it. This suite is the ONLY enforcement of the
# backend-parity rule, so if you are changing store behaviour, run it.
#
# It covers internal/jobs too, for the same reason: the job queue's memory and
# postgres backends must satisfy one conformance suite, and a memory-only run
# cannot see backend-specific breakage. Two such bugs were caught this way — a
# NUL byte in the dedupe fingerprint that PostgreSQL rejects outright, and an
# attempt counter kept in the job payload, which the memory backend preserves
# and postgres discards (so RetryNever ran four times).
test-postgres:
    @echo "Running postgres-tagged tests (needs RELA_TEST_DATABASE_URL)..."
    go test -race -tags postgres ./internal/store/pgstore/... ./internal/jobs/...
    go test -race -tags postgres -run 'TestConcurrency_' ./internal/entitymanager/
    go test -race -tags postgres -run 'TestWebhookConflict' ./internal/dataentry/

# Run the attachment scan tests against a REAL clamd (needs clamav-daemon
# running locally). Every other scan test substitutes a stub `sh -c` command,
# which exercises rela's plumbing but cannot see that clamdscan needs its config
# file bound into the sandbox, or that --fdpass cannot cross the namespace
# boundary — the bugs behind TKT-ZP1EE3, which shipped with a fully green suite.
#
# Opt-in via RELA_TEST_CLAMD (like RELA_TEST_DATABASE_URL) so it is a failure,
# not a skip, when you asked for it. Covers the scan command through the real
# sandbox; for the systemd layer use `just test-clamav-vm`.
test-clamav:
    @echo "Running clamd integration tests (needs a running clamav-daemon)..."
    RELA_TEST_CLAMD=1 go test -race -run 'TestClamd' -v ./internal/attachment/...

# Full end-to-end verification in a throwaway Debian VM: real clamd, real
# bubblewrap, the systemd unit AS PUBLISHED in the guide, real HTTP uploads.
#
# This is the only thing that covers the systemd layer. A conventionally
# hardened unit silently disables the sandbox, and because scanning is
# fail-closed the symptom is "every upload rejected" — not a crash. The script
# also breaks each of the three directives in turn and asserts that rela still
# fails closed and still warns.
#
# Needs limactl (brew install lima). First run downloads a Debian image and the
# ClamAV signature databases; the VM is reused afterwards.
test-clamav-vm:
    @./scripts/clamav-vm-test.sh

# Verify the binaries compile under every backend build tag. Cheap guard
# that no build-tag seam drifted; mirrors the CI compile matrix.
build-check-tags:
    @echo "Compiling all backend build-tag combinations..."
    go build ./...
    go build -tags memorybackend ./...
    go build -tags postgres ./...
    go build -tags sqlite ./...
    @echo "All build-tag combinations compile."

# ── E2E Tests ──

# Install E2E test dependencies
e2e-install:
    @echo "Installing E2E test dependencies..."
    cd e2e && npm install
    cd e2e && npx playwright install chromium

# Run E2E tests (tests data entry UI via rela-server)
e2e: build-server-e2e
    @echo "Running E2E tests..."
    cd e2e && npm test

# Run E2E tests in headed mode (visible browser)
e2e-headed: build-server-e2e
    @echo "Running E2E tests (headed)..."
    cd e2e && npm run test:headed

# Run E2E tests with Playwright UI
e2e-ui: build-server-e2e
    @echo "Running E2E tests with Playwright UI..."
    cd e2e && npm run test:ui

# Run tests with coverage profile
test-coverage:
    @echo "Running tests with coverage..."
    go test -race -shuffle=on -coverprofile=coverage.out -covermode=atomic {{go_packages}}

# Generate and display coverage report
coverage: test-coverage
    @echo "Generating coverage report..."
    go tool cover -func=coverage.out

# Check coverage meets floor thresholds (uses go-test-coverage)
coverage-check: test-coverage
    @echo "Checking coverage thresholds..."
    go-test-coverage --config=.testcoverage.yml

# Generate HTML coverage report
coverage-html: test-coverage
    @echo "Generating HTML coverage report..."
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report generated: coverage.html"

# Reachability floor: every line executed at least once by ANY test (unit +
# cross-package + postgres-tagged + e2e), or explicitly dismissed with a reasoned
# // coverage-ignore. This is a FLOOR, not a quality gate — see scripts/reachability.sh.
# Report-only for now (no threshold): establishes the honest baseline. Set
# RUN_E2E=1 / RELA_TEST_DATABASE_URL to include those legs.
reachability:
    ./scripts/reachability.sh

# Run fuzz tests (30 seconds each)
fuzz:
    @echo "Running fuzz tests..."
    go test -run='^$$' -fuzz='^FuzzParseDocument$$' -fuzztime=30s ./internal/markdown/
    go test -run='^$$' -fuzz='^FuzzParseEntityID$$' -fuzztime=30s ./internal/entity/
    go test -run='^$$' -fuzz='^FuzzValidateID$$' -fuzztime=30s ./internal/entity/

# Run the hot-path benchmarks (dry-run validation, affordance verdicts,
# search, write-path validation, plus the pre-existing markdown-parse
# benchmark in internal/lua). The pgstore graphquery benchmark is
# DB-gated and postgres-tagged — run it via:
#   go test -tags postgres -run='^$' -bench=. ./internal/store/pgstore/
bench:
    @echo "Running benchmarks..."
    go test -run='^$$' -bench=. -benchmem ./internal/entitymanager/ ./internal/affordances/ ./internal/search/ ./internal/validation/ ./internal/lua/

# Run quick fuzz tests (5 seconds each)
fuzz-short:
    @echo "Running quick fuzz tests..."
    go test -run='^$$' -fuzz='^FuzzParseDocument$$' -fuzztime=5s ./internal/markdown/
    go test -run='^$$' -fuzz='^FuzzParseEntityID$$' -fuzztime=5s ./internal/entity/
    go test -run='^$$' -fuzz='^FuzzValidateID$$' -fuzztime=5s ./internal/entity/

# Run EVERY fuzz target briefly (discovery-based; the weekly CI sweep
# runs this with the default budget). KNOWN RED until BUG-RHFHTH is
# fixed: FuzzGenerateShortID reliably finds the GenerateShortID
# prefix-validation bug.
fuzz-all fuzztime="25s":
    FUZZTIME='{{fuzztime}}' scripts/fuzz-all.sh

# ── Lint & Format ──

# Run Go linter
lint:
    @echo "Running Go linter..."
    golangci-lint run

# Check for known vulnerabilities (govulncheck with OSV filter)
govulncheck:
    @echo "Running govulncheck..."
    scripts/govulncheck-filtered.sh

# Check architecture boundaries
arch-lint:
    @echo "Checking architecture boundaries..."
    go-arch-lint check

# Compile build-tag-gated Go files — the default build skips them, so they rot
tagged-tests:
    @echo "Compiling build-tag-gated files..."
    scripts/check-tagged-tests.sh

# Test the tagged-build guard itself (run this after changing its parser)
tagged-tests-test:
    @echo "Testing the tagged-build guard..."
    scripts/check-tagged-tests-test.sh

# Check type load lines (god-object linter). Existing offenders are
# grandfathered with //plimsoll:max-* directives at the declaration site;
# ratchet those down over time (TKT-N0IKN9). Keep plimsoll_version in sync
# with the install pin in .github/workflows/ci.yml.
plimsoll_version := "v0.2.0"
plimsoll:
    @echo "Checking type load lines (god-object lint)..."
    go run github.com/sourcehaven-bv/plimsoll/cmd/plimsoll@{{plimsoll_version}} ./...

# Run linter with auto-fix
lint-fix:
    @echo "Running linter with auto-fix..."
    golangci-lint run --fix

# Lint markdown files
lint-md:
    @echo "Linting markdown files..."
    npx markdownlint-cli2 "**/*.md" "#node_modules" "#**/node_modules"

# Lint and fix markdown files
lint-md-fix:
    @echo "Linting and fixing markdown files..."
    npx markdownlint-cli2 --fix "**/*.md" "#node_modules" "#**/node_modules"

# Format Go code
fmt:
    @echo "Formatting code..."
    go fmt ./...
    goimports -w -local github.com/Sourcehaven-BV/rela .

# Run go vet
vet:
    @echo "Running go vet..."
    go vet ./...

# ── CI & Checks ──

# Comment discipline. The gate (commented-code) is clean and enforced in CI;
# the report surfaces the advisory rules whose backlog is still being worked
# down. Keep commentlint_version in sync with .github/workflows/ci.yml.
commentlint_version := "v0.3.1"
comment-lint:
    @echo "==> commentlint (gate)"
    go run github.com/sourcehaven-bv/commentlint@{{commentlint_version}} -rules commented-code,doclink ./internal ./cmd

# One invocation per rule, because the cross-comment rules replace the
# per-comment output rather than adding to it — a single run would silently
# report only one of them.
#
# Advisory comment findings, worst-first. Never fails; this is a worklist.
comment-report rule="":
    #!/usr/bin/env bash
    set -uo pipefail
    if [ -n "{{rule}}" ]; then
        go run github.com/sourcehaven-bv/commentlint@{{commentlint_version}} \
            -rules "{{rule}}" -rank -top 40 ./internal ./cmd
        exit 0
    fi
    for rule in restatement param-contract nil-contract duplication; do
        echo "==> commentlint $rule"
        go run github.com/sourcehaven-bv/commentlint@{{commentlint_version}} \
            -rules "$rule" -rank -top 40 ./internal ./cmd || true
    done

# Run all checks (lint + arch-lint + lint-md + test)
check: lint arch-lint plimsoll comment-lint lint-md test

# Regenerate everything derived from the canonical icon table
# (internal/dataentryconfig/icondefs): the Go allowlist, the SPA registry, and
# the documentation table.
#
# The docs half writes into the GUIDE ENTITY, which `just docs` then renders to
# docs/data-entry.md — hence the ordering in `docs` below. Writing to docs/
# directly would be reverted on the next docs run.
generate-icons:
    @go run ./cmd/gen-icons -root "{{justfile_directory()}}"

# Generate docs from rela entities via mdcomp
docs: build-cli generate-icons
    @echo "Generating documentation..."
    @./scripts/generate-docs.sh

# Build the worlds manual WITH screenshots and open it as HTML for visual
# inspection. This is the "did the UI actually render what the prose claims"
# loop: every assertion in the manual has already passed by the time you see
# the page, so what you are inspecting is the part a machine cannot check —
# whether the figures are legible and show what their captions say.
#
# Needs a built frontend (the screenshots drive the real SPA) and Chrome.
# Output lands in .ignored/ because the PNGs are not byte-reproducible and
# nothing here is committed.
#
# `just docs-visual open=1` also opens it in the default browser.
docs-visual open="0": build-docs build-frontend
    #!/usr/bin/env bash
    set -euo pipefail
    # The worlds manual documents version history, which only pgstore
    # implements — so this fs target cannot build it and says so up front
    # rather than creating an output directory it will never fill. Building
    # anyway would either fail halfway or publish a "history is not available"
    # screenshot under prose describing a populated timeline.
    echo "The worlds manual needs the postgres backend (version history)." >&2
    echo "" >&2
    echo "  RELA_DATABASE_URL='postgres:///rela_docs?host=/tmp' just docs-visual-postgres" >&2
    exit 1

# Render the worlds manual against POSTGRES, so the History section captures a
# real, populated version timeline instead of "not available for this
# deployment".
#
# Requires RELA_DATABASE_URL (env-only by design — there is deliberately no
# DSN flag, so the credential never lands in `ps` or shell history). The
# database only needs to be reachable and writable: the manual's fixture goes
# into a private scratch schema that is created for the build and dropped
# after it.
#
# The sweep cadence overrides are what make the History figures possible in a
# docs build at all. On postgres, create/update versions are captured by a
# DEBOUNCED reconciliation sweep whose defaults are 5m idle / 5m interval —
# entirely right for production and far longer than any build. Lowering them
# does not fake anything: the same sweep does the same capture, just sooner.
# The capture then WAITS on the history API reporting the rows the manual
# claims, so the figure cannot be photographed early.
docs-visual-postgres open="0": build-docs-postgres build-frontend
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -z "${RELA_DATABASE_URL:-}" ]; then
        echo "RELA_DATABASE_URL is not set." >&2
        echo "" >&2
        echo "The postgres manual build needs a database to create its scratch" >&2
        echo "schema in. Example:" >&2
        echo "" >&2
        echo "  RELA_DATABASE_URL='postgres:///rela_docs?host=/tmp' just docs-visual-postgres" >&2
        exit 1
    fi
    out=".ignored/worlds-manual-postgres"
    mkdir -p "$out"
    echo "Building the worlds manual against postgres (with screenshots)..."
    RELA_VERSION_SWEEP_INTERVAL=500ms \
    RELA_VERSION_SWEEP_IDLE=200ms \
    RELA_VERSION_SWEEP_MAX_STALENESS=2s \
    ./bin/rela-docs-postgres build prototypes/worlds/manual/worlds-manual.md \
        --project prototypes/worlds/project \
        --out "$out/worlds-manual.md"
    if command -v pandoc >/dev/null 2>&1; then
        python3 scripts/manual-html.py "$out/worlds-manual.md" "$out/worlds-manual.html"
        if [ "{{open}}" != "0" ]; then open "$out/worlds-manual.html"; fi
    else
        echo "✓ $out/worlds-manual.md (install pandoc for the HTML render)"
    fi

# Regenerate the example operator handbook (docs/examples/) from the demo
# project by building its rela-docs manual. Kept OUT of `docs`/`docs-check`:
# it needs the rela-docs binary (frontend + Chrome for the screenshot) and the
# PNG is not byte-reproducible, so the committed output is the source of truth
# and this is an opt-in, run-when-you-change-the-manual target.
docs-example: build-docs
    @echo "Building the example ticket-tracker handbook..."
    ./bin/rela-docs build prototypes/data-entry/manual/tickets-manual.md \
        --project prototypes/data-entry/project \
        --out docs/examples/ticket-tracker-manual.md
    @echo "✓ Wrote docs/examples/ticket-tracker-manual.md (+ ticket-form.png)"

# Check that committed docs are up to date with entities
docs-check: docs
    @echo "Checking docs are up to date..."
    git diff --exit-code docs/ README.md docs-project/ || \
        (echo "" && echo "ERROR: docs/, README.md or docs-project/ is out of date." && \
         echo "Run 'just docs' and commit the changes." && exit 1)
    @echo "✓ Docs are up to date."

# Run full CI pipeline (check + coverage + build + docs)
ci: check coverage-check build docs-check

# ── Dependencies & Tools ──

# Install development tools
install-tools:
    @echo "Installing development tools..."
    @echo "Installing golangci-lint {{golangci_lint_version}} (same install path as CI)..."
    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{golangci_lint_version}}
    @echo "Installing goimports..."
    go install golang.org/x/tools/cmd/goimports@latest
    @echo "Installing go-test-coverage..."
    go install github.com/vladopajic/go-test-coverage/v2@latest
    @echo "Installing go-arch-lint {{go_arch_lint_version}}..."
    go install github.com/fe3dback/go-arch-lint@{{go_arch_lint_version}}
    @echo "Done!"

# Install git hooks
install-hooks:
    @echo "Installing git hooks..."
    @mkdir -p .git/hooks
    @cp scripts/pre-commit .git/hooks/pre-commit
    @chmod +x .git/hooks/pre-commit
    @cp scripts/pre-push .git/hooks/pre-push
    @chmod +x .git/hooks/pre-push
    @echo "Git hooks installed (pre-commit + pre-push)!"

# Tidy go modules
tidy:
    @echo "Tidying modules..."
    go mod tidy

# Download dependencies
deps:
    @echo "Downloading dependencies..."
    go mod download

# ── Icons ──

# Source SVG and output directories
logo_svg := "build/package/logo.svg"
icon_tmp := "build/package/.icon-tmp"

# Generate all app icons from logo.svg (requires rsvg-convert, imagemagick, iconutil)
icons: _icon-pngs _icon-icns _icon-ico _icon-linux
    @rm -rf {{icon_tmp}}
    @echo "All icons generated. Review changes with 'git diff' and commit."

# Generate intermediate PNGs at all required sizes
_icon-pngs:
    @mkdir -p {{icon_tmp}}
    @echo "Generating PNGs from {{logo_svg}}..."
    @for size in 16 32 48 64 128 256 512 1024; do \
        rsvg-convert -w $size -h $size -b '#031b75' {{logo_svg}} -o {{icon_tmp}}/icon_${size}.png; \
    done

# Generate the macOS icon art: a rounded-rect ("squircle") on transparent
# margin. macOS draws app icons inside its own grid, so full-bleed art gets
# inset again and reads as a small square inside a larger one. Apple's grid
# puts a rounded-rect at 824/1024 of the canvas, so the art is scaled to ~80%
# and centred, with the remaining ~10% each side left transparent.
#
# Windows and Linux want full-bleed art and keep using the _icon-pngs output.
_icon-macos-pngs: _icon-pngs
    @echo "Generating macOS squircle PNGs..."
    @for size in 16 32 64 128 256 512 1024; do \
        inner=$(( size * 824 / 1024 )); \
        radius=$(( inner * 2237 / 10000 )); \
        magick {{icon_tmp}}/icon_${size}.png -alpha set -resize ${inner}x${inner}! \
            {{icon_tmp}}/art_${size}.png; \
        magick -size ${inner}x${inner} xc:none -fill white \
            -draw "roundrectangle 0,0,$((inner-1)),$((inner-1)),${radius},${radius}" \
            {{icon_tmp}}/mask_${size}.png; \
        magick {{icon_tmp}}/art_${size}.png {{icon_tmp}}/mask_${size}.png \
            -alpha set -compose DstIn -composite \
            -compose Over \
            -background none -gravity center -extent ${size}x${size} \
            {{icon_tmp}}/mac_${size}.png; \
    done

# Generate macOS .icns (requires macOS iconutil)
_icon-icns: _icon-macos-pngs
    @echo "Generating macOS .icns..."
    @mkdir -p {{icon_tmp}}/rela-desktop.iconset
    @cp {{icon_tmp}}/mac_16.png   {{icon_tmp}}/rela-desktop.iconset/icon_16x16.png
    @cp {{icon_tmp}}/mac_32.png   {{icon_tmp}}/rela-desktop.iconset/icon_16x16@2x.png
    @cp {{icon_tmp}}/mac_32.png   {{icon_tmp}}/rela-desktop.iconset/icon_32x32.png
    @cp {{icon_tmp}}/mac_64.png   {{icon_tmp}}/rela-desktop.iconset/icon_32x32@2x.png
    @cp {{icon_tmp}}/mac_128.png  {{icon_tmp}}/rela-desktop.iconset/icon_128x128.png
    @cp {{icon_tmp}}/mac_256.png  {{icon_tmp}}/rela-desktop.iconset/icon_128x128@2x.png
    @cp {{icon_tmp}}/mac_256.png  {{icon_tmp}}/rela-desktop.iconset/icon_256x256.png
    @cp {{icon_tmp}}/mac_512.png  {{icon_tmp}}/rela-desktop.iconset/icon_256x256@2x.png
    @cp {{icon_tmp}}/mac_512.png  {{icon_tmp}}/rela-desktop.iconset/icon_512x512.png
    @cp {{icon_tmp}}/mac_1024.png {{icon_tmp}}/rela-desktop.iconset/icon_512x512@2x.png
    @iconutil -c icns {{icon_tmp}}/rela-desktop.iconset -o build/package/macos/rela-desktop.icns

# Generate Windows .ico (requires imagemagick)
_icon-ico: _icon-pngs
    @echo "Generating Windows .ico..."
    @magick {{icon_tmp}}/icon_16.png {{icon_tmp}}/icon_32.png {{icon_tmp}}/icon_48.png \
            {{icon_tmp}}/icon_64.png {{icon_tmp}}/icon_128.png {{icon_tmp}}/icon_256.png \
            build/package/windows/rela-desktop.ico

# Generate Linux PNGs
_icon-linux: _icon-pngs
    @echo "Generating Linux PNGs..."
    @cp {{icon_tmp}}/icon_256.png build/package/linux/rela-desktop.png
    @cp {{icon_tmp}}/icon_512.png build/package/linux/rela-desktop-512.png

# ── Dev Server ──

# Run the data entry server for development (ticketing example)
[no-exit-message]
dev project="prototypes/data-entry/project" port="8080":
    go run ./cmd/rela-server -project {{project}} -port {{port}}

# Run the catalog example
[no-exit-message]
dev-catalog port="8282":
    go run ./cmd/rela-server -project prototypes/data-entry/catalog -port {{port}}

# ── Frontend Dev ──

# Run Vue dev server with hot-reloading (requires Go server running on :8080)
[no-exit-message]
dev-frontend:
    cd frontend && npm run dev

# Install frontend dependencies
install-frontend:
    cd frontend && npm install

# Build Vue frontend for production
build-frontend: install-frontend
    cd frontend && npm run build

# Build Vue frontend in development mode for E2E. This bundle has
# import.meta.env.DEV === true, so DEV-guarded test hooks (e.g. the
# backtick-autocomplete delay knob, issue #890) compile in. Production
# builds use `build-frontend`, which strips them.
build-frontend-e2e: install-frontend
    cd frontend && npm run build:e2e

# Type-check Vue frontend
typecheck-frontend:
    cd frontend && npm run typecheck

# Lint Vue frontend
lint-frontend:
    cd frontend && npm run lint
