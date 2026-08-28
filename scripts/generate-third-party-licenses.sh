#!/bin/sh
# Generate the complete third-party license compendium for shipped binaries.
set -eu

OUTPUT="${1:-THIRD_PARTY_LICENSES.txt}"
TEMP_FILE=$(mktemp)
trap 'rm -f "$TEMP_FILE"' EXIT

cat >"$TEMP_FILE" <<'HEADER'
MINDMORY THIRD-PARTY LICENSES
================================

This file contains the complete upstream license and notice files distributed
with every non-standard Go module compiled into the Mindmory release binaries.
Module versions are resolved from the current Go module graph.

Mindmory itself is licensed separately under the MIT License in LICENSE.
HEADER

go_root=$(go env GOROOT)
go_release=$(go env GOVERSION)
go_license="$go_root/LICENSE"
[ -f "$go_license" ] || go_license="$(dirname "$go_root")/LICENSE"
[ -f "$go_license" ] || {
  printf 'Go toolchain license not found for %s\n' "$go_root" >&2
  exit 1
}
{
  printf '\n\n-------------------------------------------------------------------------------\n'
  printf 'Component: Go runtime and standard library\n'
  printf 'Version: %s\n' "$go_release"
  printf 'Upstream file: LICENSE\n'
  printf '%s\n\n' '-------------------------------------------------------------------------------'
  cat "$go_license"
} >>"$TEMP_FILE"
if [ -f "$go_root/PATENTS" ]; then
  {
    printf '\n\n-------------------------------------------------------------------------------\n'
    printf 'Component: Go runtime and standard library\n'
    printf 'Version: %s\n' "$go_release"
    printf 'Upstream file: PATENTS\n'
    printf '%s\n\n' '-------------------------------------------------------------------------------'
    cat "$go_root/PATENTS"
  } >>"$TEMP_FILE"
fi

go list -deps -f '{{with .Module}}{{if and (not .Main) .Dir}}{{printf "%s\t%s\t%s" .Path .Version .Dir}}{{end}}{{end}}' \
  ./cmd/mindmoryd-lite ./cmd/mindmoryctl ./cmd/mindmory-mcp-stdio |
  sort -u |
  while IFS="$(printf '\t')" read -r module version module_dir; do
    [ -n "$module" ] || continue
    license_files=$(find "$module_dir" -maxdepth 2 -type f \( -iname 'license*' -o -iname 'copying*' -o -iname 'notice*' \) | sort)
    [ -n "$license_files" ] || {
      printf 'No license file found for %s %s\n' "$module" "$version" >&2
      exit 1
    }
    for license_file in $license_files; do
      relative=${license_file#"$module_dir"/}
      {
        printf '\n\n-------------------------------------------------------------------------------\n'
        printf 'Module: %s\n' "$module"
        printf 'Version: %s\n' "$version"
        printf 'Upstream file: %s\n' "$relative"
        printf '%s\n\n' '-------------------------------------------------------------------------------'
        cat "$license_file"
      } >>"$TEMP_FILE"
    done
  done

mv "$TEMP_FILE" "$OUTPUT"
trap - EXIT
