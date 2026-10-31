#!/usr/bin/env bash
# Guard: the checks that keep a public, CI-gated repository honest.
#
#   scripts/guard.sh embed   only the placeholder may be committed under the embed directory
#   scripts/guard.sh seeds   every fuzz target has a seed corpus
#   scripts/guard.sh live    no test can reach a live provider
#   scripts/guard.sh all     all of the above
#
# The secret scan is not here: CI runs the gitleaks action on the pushed range and `make gitleaks` runs it
# locally. One scanner in one place, so the two can never disagree about the version or the rules.
#
# Run from the repository root. Exits non-zero on the first failure, with the name of the rule that broke.

set -euo pipefail

EMBED_DIR="internal/webui/dist"
fail() { printf 'guard: FAIL %s\n' "$*" >&2; exit 1; }

check_embed() {
  # The embed directory is built into the binary. Committing a build there would hide a stale bundle in the
  # repo and let a build artifact drift from its source, so only the placeholder may be tracked.
  local tracked
  tracked="$(git ls-files -- "$EMBED_DIR")"
  local extra
  extra="$(printf '%s\n' "$tracked" | grep -v -x -e "$EMBED_DIR/.gitkeep" -e "" || true)"
  if [ -n "$extra" ]; then
    printf 'tracked files that must not be committed under %s:\n%s\n' "$EMBED_DIR" "$extra" >&2
    fail "embed placeholder"
  fi
  if [ ! -f "$EMBED_DIR/.gitkeep" ]; then
    fail "embed placeholder: $EMBED_DIR/.gitkeep is missing"
  fi
  echo "guard: embed placeholder ok"
}

check_seeds() {
  # A brand-new fuzz target without a corpus spends its first minutes rediscovering the obvious. Every target
  # must ship seeds, either as f.Add calls in the test or as files under testdata/fuzz/<Name>.
  local missing=0
  while IFS= read -r target; do
    [ -n "$target" ] || continue
    local file pkg dir
    file="$(grep -rlE "^func ${target}\(" --include='*_test.go' . | head -1)"
    [ -n "$file" ] || { echo "guard: cannot find ${target}" >&2; missing=1; continue; }
    pkg="$(dirname "$file")"
    dir="$pkg/testdata/fuzz/$target"
    if grep -qE '^\s*f\.Add\(' "$file" || { [ -d "$dir" ] && [ -n "$(ls -A "$dir")" ]; }; then
      echo "guard: seed corpus ok for $target"
    else
      echo "guard: no seed corpus for $target ($file)" >&2
      missing=1
    fi
  done < <(grep -rhoE '^func (Fuzz[A-Za-z0-9_]+)' --include='*_test.go' . | awk '{print $2}' | sort -u)
  [ "$missing" -eq 0 ] || fail "fuzz seed corpora"
}

check_live() {
  # Nothing in a test may reach a live provider: the world's fakes are the only allowed peer. This looks for
  # the three shapes a live call takes, not for the words, so a fixture may still name a host.
  local hits
  hits="$(grep -rnE \
      -e '\bhttp\.(Get|Post|Head)\(' \
      -e 'http\.DefaultClient' \
      -e 'net\.Dial\(|net\.DialTimeout\(' \
      --include='*_test.go' . || true)"
  if [ -n "$hits" ]; then
    printf 'tests that dial the network directly:\n%s\n' "$hits" >&2
    fail "live provider in tests"
  fi
  # A test that names a real endpoint is almost always a test that means to call it.
  local hosts
  hosts="$(grep -rnE \
      -e 'https://(api\.themoviedb\.org|openlibrary\.org|openrouter\.ai)' \
      --include='*_test.go' . | grep -v 'internal/world' || true)"
  if [ -n "$hosts" ]; then
    printf 'tests naming a live endpoint:\n%s\n' "$hosts" >&2
    fail "live provider in tests"
  fi
  echo "guard: no live provider in tests"
}

case "${1:-all}" in
  embed) check_embed ;;
  seeds) check_seeds ;;
  live)  check_live ;;
  all)
    check_embed
    check_seeds
    check_live
    echo "guard: all green"
    ;;
  *) echo "usage: $0 [embed|seeds|live|all]" >&2; exit 2 ;;
esac
