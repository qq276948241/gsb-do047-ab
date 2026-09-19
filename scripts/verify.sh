#!/usr/bin/env bash
#
# verify.sh - one-command acceptance check for mdschema.
#
# Runs the full pipeline in order and stops at the first failing step:
#   1. toolchain check   - the Go version must match go.mod (go directive)
#   2. dependency lock   - go.mod and go.sum must agree; deps download & verify
#   3. build             - compile the mdschema binary
#   4. test              - run the whole test suite
#   5. example check     - validate every shipped example with its schema
#   6. example generate  - generated templates must match the golden snapshots
#
# Usage:
#   scripts/verify.sh             run the full acceptance pipeline
#   scripts/verify.sh --update    regenerate golden snapshots instead of diffing
#
# Designed for a clean machine: it needs only a Go toolchain on PATH. The exact
# Go version and every third-party dependency are pinned in go.mod and go.sum.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

UPDATE_GOLDEN=false
if [[ "${1:-}" == "--update" ]]; then
	UPDATE_GOLDEN=true
fi

# Never let go commands silently edit the committed lock files.
export GOFLAGS="-mod=readonly"

# Pin the exact toolchain version declared in go.mod. A newer local Go still
# runs the pinned version (downloading it once if needed); it never drifts up.
WANT_GO="$(awk '/^go [0-9]+\.[0-9]+/{print $2; exit}' go.mod)"
if [[ -z "$WANT_GO" ]]; then
	printf '\033[1;31mverify failed: could not read the go directive from go.mod\033[0m\n' >&2
	exit 1
fi
export GOTOOLCHAIN="go${WANT_GO}"

BIN_DIR="$(mktemp -d)"
GEN_DIR="$(mktemp -d)"
LOCK_LOG="$(mktemp)"
DOWNLOAD_LOG="$(mktemp)"
trap 'rm -rf "$BIN_DIR" "$GEN_DIR" "$LOCK_LOG" "$DOWNLOAD_LOG"' EXIT
BIN="$BIN_DIR/mdschema"

step() {
	printf '\n\033[1;36m==> %s\033[0m\n' "$1"
}

fail() {
	printf '\033[1;31mverify failed: %s\033[0m\n' "$1" >&2
	exit 1
}

# name the dependency behind a go.sum/lock error whenever Go does not
dependency_hint() {
	local dep
	dep="$(sed -n 's/^.*missing go.sum entry for module providing package \([^ )]*\).*$/\1/p' "$1" | head -1)"
	if [[ -z "$dep" ]]; then
		dep="$(sed -n 's/^\s*\([^ ]*\)@[^:]*: .*$/\1/p' "$1" | grep -v '^github.com/jackchuka/mdschema' | head -1)"
	fi
	if [[ -n "$dep" ]]; then
		printf '\033[1;31mdeclaration/lock mismatch for dependency: %s\033[0m\n' "$dep" >&2
		printf 'run `go mod tidy` and commit the updated go.mod / go.sum\n' >&2
	fi
}

# --- 1. toolchain -----------------------------------------------------------
step "1/6 checking Go toolchain against go.mod"
GOT_GO="$(go env GOVERSION | sed 's/^go//')"
if [[ "$GOT_GO" != "$WANT_GO"* ]]; then
	fail "go$WANT_GO is pinned in go.mod but go resolved go$GOT_GO"
fi
echo "go$GOT_GO matches go.mod (go $WANT_GO)"

# --- 2. dependency lock -----------------------------------------------------
step "2/6 checking dependency lock (go.mod <-> go.sum)"

# Offline resolution under -mod=readonly: a missing go.sum entry or a go.mod
# requirement whose locked module is unavailable fails and names the dependency.
if ! GOPROXY=off go list -mod=readonly -deps ./... >/dev/null 2>>"$LOCK_LOG"; then
	cat "$LOCK_LOG" >&2
	dependency_hint "$LOCK_LOG"
	fail "dependency declaration (go.mod) and lock file (go.sum) disagree"
fi

echo "installing dependencies at locked versions"
if ! go mod download all >>"$DOWNLOAD_LOG" 2>&1; then
	if grep -q 'dial tcp\|i/o timeout' "$DOWNLOAD_LOG"; then
		echo "skipping dependency download (no network); using cached modules"
	else
		cat "$DOWNLOAD_LOG" >&2
		dependency_hint "$DOWNLOAD_LOG"
		fail "could not download locked dependencies"
	fi
else
	echo "verifying dependency checksums against go.sum"
	if ! go mod verify >>"$DOWNLOAD_LOG" 2>&1; then
		cat "$DOWNLOAD_LOG" >&2
		fail "dependency verification failed; module cache does not match go.sum"
	fi
fi
echo "all dependencies locked and verified"

# --- 3. build ---------------------------------------------------------------
step "3/6 building mdschema"
go build -o "$BIN" ./cmd/mdschema
echo "built $BIN"

# --- 4. test ----------------------------------------------------------------
step "4/6 running tests"
go test ./...

# --- 5. example check -------------------------------------------------------
step "5/6 checking shipped examples"
# pairs of <markdown file>:<schema file>
EXAMPLES=(
	"README.md:examples/README.mdschema.yml"
	"examples/requirements.md:examples/requirements.mdschema.yml"
	"examples/tutorial.md:examples/tutorial.mdschema.yml"
	"examples/blog-post.md:examples/blog-post.mdschema.yml"
)
for pair in "${EXAMPLES[@]}"; do
	md="${pair%%:*}"
	schema="${pair##*:}"
	echo "check $md --schema $schema"
	"$BIN" check "$md" --schema "$schema"
done

# --- 6. example generate ----------------------------------------------------
step "6/6 generating example templates"
GOLDEN_DIR="examples/golden"
SCHEMAS=(
	"README:examples/README.mdschema.yml"
	"requirements:examples/requirements.mdschema.yml"
	"tutorial:examples/tutorial.mdschema.yml"
	"blog-post:examples/blog-post.mdschema.yml"
)
for pair in "${SCHEMAS[@]}"; do
	name="${pair%%:*}"
	schema="${pair##*:}"
	out="$GEN_DIR/$name.md"
	golden="$GOLDEN_DIR/$name.golden.md"
	echo "generate $schema"
	"$BIN" generate "$schema" -o "$out"
	if $UPDATE_GOLDEN; then
		mkdir -p "$GOLDEN_DIR"
		cp "$out" "$golden"
		echo "updated $golden"
	elif ! diff -u "$golden" "$out"; then
		fail "generated output for $name drifted from $golden (run 'scripts/verify.sh --update' if the change is intended)"
	fi
done

printf '\n\033[1;32mverify OK: lock, build, tests, example checks and generation all passed\033[0m\n'
