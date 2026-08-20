#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
LOCK="$ROOT/web/static/assets.lock"
FONT_UNICODES='U+0000-00FF,U+2000-206F,U+2190-21FF,U+2500-257F,U+2580-259F,U+E0A0-E0D4'

die() {
  printf 'web-assets: %s\n' "$*" >&2
  exit 1
}

require_commands() {
  local command
  for command in "$@"; do
    command -v "$command" >/dev/null 2>&1 || die "required command not found: $command"
  done
}

file_hash() {
  sha256sum "$1" | awk '{print $1}'
}

tree_hash() {
  (
    cd "$1"
    # LC_ALL=C keeps the file order byte-collated. Without it, a UTF-8
    # collation locale reorders the names and the same tree hashes
    # differently, so verify fails against a lock recorded elsewhere.
    LC_ALL=C
    export LC_ALL
    find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | awk '{print $1}'
  )
}

replace_hash() {
  local target=$1 hash=$2 temporary
  temporary=$(mktemp "$LOCK.XXXXXX")
  awk -F'|' -v OFS='|' -v target="$target" -v hash="$hash" '
    /^#/ || NF < 5 { print; next }
    $2 == target { $5 = hash }
    { print }
  ' "$LOCK" >"$temporary"
  mv "$temporary" "$LOCK"
}

verify_file() {
  local target=$1 expected=$2 actual
  [ -f "$ROOT/$target" ] || die "missing asset: $target"
  actual=$(file_hash "$ROOT/$target")
  [ "$actual" = "$expected" ] || die "checksum mismatch: $target (expected $expected, got $actual)"
}

verify_tree() {
  local target=$1 expected=$2 actual
  [ -d "$ROOT/$target" ] || die "missing asset tree: $target"
  actual=$(tree_hash "$ROOT/$target")
  [ "$actual" = "$expected" ] || die "tree checksum mismatch: $target (expected $expected, got $actual)"
}

verify() {
  local kind target source member expected
  while IFS='|' read -r kind target source member expected; do
    [ -n "$kind" ] || continue
    case "$kind" in
      \#*) continue ;;
      file|font) verify_file "$target" "$expected" ;;
      tree) verify_tree "$target" "$expected" ;;
      *) die "unknown lock entry type: $kind" ;;
    esac
  done <"$LOCK"
  printf 'web-assets: verified %s\n' "$LOCK"
}

download() {
  curl --fail --location --retry 2 --silent --show-error --output "$2" "$1"
}

update_file() {
  local target=$1 source=$2 temporary hash
  temporary=$(mktemp)
  download "$source" "$temporary"
  mkdir -p "$(dirname "$ROOT/$target")"
  mv "$temporary" "$ROOT/$target"
  hash=$(file_hash "$ROOT/$target")
  replace_hash "$target" "$hash"
  printf 'web-assets: updated %s (%s)\n' "$target" "$hash"
}

update_font() {
  local target=$1 source=$2 member=$3 directory temporary input subset output hash
  directory=$(mktemp -d)
  temporary="$directory/font.tar.xz"
  download "$source" "$temporary"
  tar -xJf "$temporary" -C "$directory" "$member"
  input="$directory/$member"
  subset="$directory/subset.ttf"
  pyftsubset "$input" --unicodes="$FONT_UNICODES" --output-file="$subset"
  woff2_compress "$subset" >/dev/null
  output="$directory/subset.woff2"
  mkdir -p "$(dirname "$ROOT/$target")"
  mv "$output" "$ROOT/$target"
  rm -rf "$directory"
  hash=$(file_hash "$ROOT/$target")
  replace_hash "$target" "$hash"
  printf 'web-assets: updated %s (%s)\n' "$target" "$hash"
}

update_tree() {
  local target=$1 source=$2 directory temporary upstream hash
  directory=$(mktemp -d)
  temporary="$directory/novnc.tar.gz"
  download "$source" "$temporary"
  tar -xzf "$temporary" -C "$directory"
  upstream=$(find "$directory" -mindepth 1 -maxdepth 1 -type d -print -quit)
  [ -n "$upstream" ] || die "no extracted noVNC directory found"
  rm -rf "$ROOT/$target/core" "$ROOT/$target/vendor/pako" "$ROOT/$target/LICENSE.txt"
  mkdir -p "$ROOT/$target/vendor"
  cp -a "$upstream/core" "$ROOT/$target/core"
  cp -a "$upstream/vendor/pako" "$ROOT/$target/vendor/pako"
  cp "$upstream/LICENSE.txt" "$ROOT/$target/LICENSE.txt"
  rm -rf "$directory"
  hash=$(tree_hash "$ROOT/$target")
  replace_hash "$target" "$hash"
  printf 'web-assets: updated %s (%s)\n' "$target" "$hash"
}

update() {
  require_commands curl sha256sum awk find sort xargs tar cp rm mktemp pyftsubset woff2_compress
  local kind target source member expected
  while IFS='|' read -r kind target source member expected; do
    [ -n "$kind" ] || continue
    case "$kind" in
      \#*) continue ;;
      file) update_file "$target" "$source" ;;
      font) update_font "$target" "$source" "$member" ;;
      tree) update_tree "$target" "$source" ;;
      *) die "unknown lock entry type: $kind" ;;
    esac
  done <"$LOCK"
}

case "${1:-}" in
  verify) require_commands sha256sum awk find sort xargs; verify ;;
  update) update ;;
  *) printf 'Usage: %s verify|update\n' "$0" >&2; exit 2 ;;
esac
