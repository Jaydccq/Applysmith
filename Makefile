# Generation order matters: wire types, then frontend assets, then any Go
# package that embeds them. Every Go target that compiles ./... depends on
# `generate`, so a fresh checkout with no bundles builds with `make build`.

BUN ?= $(or $(shell command -v bun),$(HOME)/.bun/bin/bun)
BUN_VERSION := $(shell sed -n 's/.*"packageManager": "bun@\(.*\)".*/\1/p' web/package.json)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
export GOMAXPROCS ?= 4
GO_TEST_FLAGS ?= -p 2 -parallel 2
# Builds leave the checkout's path out of the binary, so every worktree at the
# same commit shares one build cache entry, and compile no DWARF, which only a
# debugger reads (docs/development.md, "Build cache").
export GOFLAGS := $(sort -trimpath -gcflags=all=-dwarf=false $(GOFLAGS))
# The suites below run one at a time on this machine: a second one waits for
# this lock instead of competing for the CPU and memory (docs/development.md,
# "One heavy run at a time").
HEAVY_LOCK ?= $(HOME)/Library/Caches/applysmith-go-heavy.lock
HEAVY_TARGETS := verify verify-documents test-browser test-browser-headed measure release-check
# The owner's personal-token list, when this machine has one, so every
# `make verify` scans for it (docs/development.md, "Inputs kept outside the
# repository"). An explicit APPLYSMITH_PII_TOKENS wins.
export APPLYSMITH_PII_TOKENS ?= $(wildcard $(HOME)/.config/applysmith/pii-tokens.txt)

LDFLAGS := -X applysmith/internal/cli.version=$(VERSION)

WEB_SRC := $(shell find web/src -type f) $(wildcard docs/setup/*.md docs/user-guide/*.md docs/user-guide/images/*.webp) web/index.html web/vite.config.ts web/tsconfig.json web/package.json web/bun.lock

.PHONY: generate build publish-tree notices test-live app app-smoke clean-generated $(HEAVY_TARGETS) $(addprefix locked-,$(HEAVY_TARGETS))

ENGINE_SRC := $(shell find engine/src -type f) engine/tsconfig.json internal/engine/bundler/bundler.go internal/engine/stamp/stamp.go internal/engine/gen.go

WIRE_SRC := $(filter-out %_test.go,$(wildcard internal/api/wire/*.go)) tygo.yaml go.mod

generate: web/src/wire/index.ts web/dist/index.html internal/engine/generated/engine.js internal/extension/generated/manifest.json

# The wire declarations come first: the generator reads only internal/api/wire,
# which embeds nothing, so it runs on a fresh checkout before any bundle exists.
web/src/wire/index.ts: $(WIRE_SRC)
	go tool tygo generate --config tygo.yaml

web/dist/index.html: $(WEB_SRC) web/src/wire/index.ts
	@test "$$($(BUN) --version)" = "$(BUN_VERSION)" || { echo "bun $(BUN_VERSION) required, found $$($(BUN) --version)"; exit 1; }
	cd web && $(BUN) install --frozen-lockfile
	cd web && $(BUN) run build
	@touch $@

# The engine bundle needs neither bun nor node_modules: esbuild reads the
# TypeScript sources directly.
internal/engine/generated/engine.js: $(ENGINE_SRC)
	go generate ./internal/engine

# The extension is built by Go as well: esbuild reads extension/src and the
# engine sources directly, so it needs no node_modules.
EXTENSION_SRC := $(shell find extension/src -type f) extension/manifest.json extension/popup.html extension/tsconfig.json internal/extension/builder/builder.go internal/extension/builder/manifest.go internal/extension/gen.go

internal/extension/generated/manifest.json: $(EXTENSION_SRC) $(ENGINE_SRC)
	go generate ./internal/extension

# Each heavy target waits for HEAVY_LOCK, then runs its locked- recipe.
$(HEAVY_TARGETS):
	@lockf -t 0 $(HEAVY_LOCK) true 2>/dev/null || echo "make $@: another heavy run holds $(HEAVY_LOCK); waiting for it"
	@lockf -k $(HEAVY_LOCK) $(MAKE) --no-print-directory locked-$@

build: generate
	go build -tags dashboard -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

# Everything that needs no browser: vet, every Go test (the release-hygiene
# guard is one of them, internal/guard), then the dashboard, engine and
# extension typechecks and tests.
locked-verify: generate
	go vet -tags dashboard ./...
	go test $(GO_TEST_FLAGS) -tags dashboard ./...
	cd web && $(BUN) run typecheck && $(BUN) run test
	cd engine && $(BUN) install --frozen-lockfile && $(BUN) run test && $(BUN) run typecheck
	cd extension && $(BUN) install --frozen-lockfile && $(BUN) run test && $(BUN) run typecheck

# Runs the installed LaTeX, Python and Poppler tools: sets up a throwaway venv
# from the locked requirements (network), compiles both synthetic templates,
# checks that adversarial content stays literal, then runs the source-to-PDF
# document flows. Not part of `make verify`: it is opt-in because it needs the
# installed tools and the network. The two packages run one after the other
# because each prepares its own toolchain root.
locked-verify-documents:
	APPLYSMITH_TEST_DOCUMENT_TOOLS=1 go test $(GO_TEST_FLAGS) -count=1 -timeout 20m -v -run '^TestToolchain_Real' ./internal/documents/toolchain/
	APPLYSMITH_TEST_DOCUMENT_TOOLS=1 go test $(GO_TEST_FLAGS) -count=1 -timeout 20m -v -run '^TestDocuments_Real' ./internal/documents/

# The publication pipeline (internal/release): verify this tree in development
# mode, scan its history for secrets, export the committed tree without class B
# paths, run the release-mode guard on the export, then build, test and package
# the export. Reads the owner's lists from APPLYSMITH_PII_TOKENS and
# APPLYSMITH_RETIRED_NAMES, reference directories from APPLYSMITH_SIMILARITY_REFERENCES,
# and needs gitleaks on PATH. Pushes nothing.
locked-release-check:
	go run ./cmd/releasecheck

# Only the export and the release-mode guard on it, into OUT (absent or empty).
publish-tree:
	@test -n "$(OUT)" || { echo "usage: make publish-tree OUT=<dir>"; exit 2; }
	go run ./cmd/releasecheck -publish-tree "$(OUT)"

# Regenerate third_party/NOTICE-dependencies.txt. Fails on an unknown or
# copyleft licence in the shipped set.
notices:
	cd web && $(BUN) install --frozen-lockfile
	APPLYSMITH_UPDATE_NOTICES=1 go test -count=1 -run '^TestUpdateNotices$$' ./internal/guard/

# Launches the installed Google Chrome, headless (--headless=new, set by the
# test harness only; the product never passes it), with throwaway profiles
# under the system temporary directory (named applysmith-browser-test-*). No
# window opens. Three test slots match the apply fixture's bounded scenario
# pool; serializing the entire matrix exceeds its package time budget.
# The API tree composes managed Chrome with real fill/session routes and
# server shutdown; its browser cases must run here too. The command line's
# browser-tagged tests (their names hold "Browser") check that a one-shot
# command stops the Chrome it launched, and the URL-only evaluation over a
# rendered page (internal/launch) runs here as well, and so does the layout
# check: every dashboard page at 360, 768 and 1280 px, failing on a document
# that scrolls sideways or a control that is cut off
# (docs/architecture/dashboard.md, "Layout check").
# A leftover test Chrome is swept even when a test fails. Each run puts its
# profiles under its own temporary directory (TMPDIR, kept short because Chrome may
# place its singleton socket there) and sweeps only the Chromes whose
# profile lies there, so a concurrent run's Chromes are never touched.
BROWSER_TEST_RUN = run=$$(mktemp -d /tmp/applysmith-tb.XXXXXX) || exit 1; rc=0; TMPDIR=$$run
BROWSER_TEST_SWEEP = pkill -f "user-data-dir=.*/$${run\#\#*/}/" || true; rm -rf "$$run"; exit $$rc
locked-test-browser: generate
	@$(BROWSER_TEST_RUN) go test -tags browser -count=1 -p 1 -parallel 3 -timeout 25m ./internal/browser/... ./internal/engine/... ./internal/extension/... ./internal/apply/... ./internal/discovery/... ./internal/api/... ./cmd/replay/... || rc=$$?; \
	go test -tags browser -count=1 -p 1 -timeout 10m -run 'Browser' ./internal/cli/ || rc=$$?; \
	TMPDIR=$$run APPLYSMITH_TEST_EVALUATION_BROWSER=1 go test -tags browser -count=1 -timeout 5m -run '^TestURLOnlyEvaluationRenderedBrowser$$' ./internal/launch/ || rc=$$?; \
	TMPDIR=$$run go test -tags 'dashboard browser' -p 1 -parallel 1 -count=1 -timeout 15m -run 'Layout' ./internal/cli/ || rc=$$?; \
	$(BROWSER_TEST_SWEEP)

# The tests whose subject is the window itself: focus, minimizing, and the
# identity a visible Chrome presents. They open real windows on the screen, so
# they are not part of test-browser; they need a GUI session. Set
# APPLYSMITH_TEST_BROWSER_RAISE=1 to also run the diagnostics that bring Chrome to
# the front.
locked-test-browser-headed:
	@$(BROWSER_TEST_RUN) APPLYSMITH_TEST_BROWSER_HEADED=1 go test -tags browser -count=1 -p 1 -parallel 1 -timeout 12m -run '^(TestE2E_Browser_(WebdriverFalseOnExplicitPort|IdentityIsNotRewritten|LaunchDoesNotStealFocus)|TestSpike_(G_|G0_|H2a_|H2b_|H3_|H6_))' ./internal/browser/ || rc=$$?; \
	$(BROWSER_TEST_SWEEP)

# Explicitly reads public sources into temporary databases, with models off.
# Limit with APPLYSMITH_LIVE_SMOKE_SOURCES; POST searches require APPLYSMITH_LIVE_SMOKE_POST=1.
test-live:
	APPLYSMITH_LIVE_SMOKE=1 go test -tags 'live browser' -p 1 -parallel 1 -count=1 -v -timeout 30m -run '^TestLiveSmoke$$' ./internal/discovery/

# Builds app/build/<os>-<arch>/Applysmith.app, ad-hoc signed, without the disk image.
app: generate
	cd app && go tool mygo build -skip-dmg

# PersonalCU, the computer-use service behind the Ask Agent's desktop tools
# (docs/architecture/computer-use.md): a Swift package built into its own app,
# computeruse/dist/PersonalCU.app, with the bundle id dev.applysmith.personal-cu,
# signed with the local identity computer-use-install creates once, or ad hoc
# without it. None of these targets is part of verify or app. The first build
# fetches the Swift packages pinned in computeruse/Package.resolved.
# computer-use-install is the one step to a running service: it creates the
# identity when there is none (that adds to the login keychain and asks for the
# login password), builds, quits and replaces /Applications/PersonalCU.app and
# opens it.
.PHONY: computer-use computer-use-test computer-use-install
computer-use:
	cd computeruse && bash scripts/build-app.sh release

computer-use-test:
	cd computeruse && bash scripts/swift-tool.sh test

computer-use-install:
	cd computeruse && bash scripts/build-app.sh install

# Records startup, process-tree memory, synthetic scan and fill completion,
# artifact sizes and test durations (cmd/measure) into
# docs/architecture/performance.json and the measured block of
# docs/architecture/performance.md. Launches the packaged app and the server on
# temporary workspaces and runs both test suites several times, so it takes
# a long while; run it on a quiet machine.
locked-measure: package build
	go run ./cmd/measure

# Builds the bundle and the disk image, ad-hoc signed, into app/build (ignored
# by git), then checks the bundle: size bound, notices, no external tools, the
# fixed identifier. The selected distribution is ad-hoc signed; quarantined
# first launch requires Open Anyway (docs/architecture/packaging.md).
.PHONY: package
package: generate
	cd app && go tool mygo build
	APPLYSMITH_APP_BUNDLE="$(CURDIR)/app/build/darwin-arm64/Applysmith.app" go test -count=1 -run '^TestPackagedBundle$$' ./app/

# The same bundle with the --smoke page script compiled in (see
# docs/architecture/app.md). Never ship it.
app-smoke: generate
	cd app && GOFLAGS="$(GOFLAGS) -tags=smoke" go tool mygo build -skip-dmg

# Captures the demo screenshots and renders the product videos into video/dist
# (both ignored by git); needs Google Chrome and ffmpeg (video/README.md).
.PHONY: videos
videos: generate
	go run ./cmd/video shots
	go run ./cmd/video render

clean-generated:
	rm -rf web/dist bin internal/engine/generated/engine.js internal/engine/generated/mainworld.js internal/extension/generated/*.js internal/extension/generated/*.json internal/extension/generated/*.html
