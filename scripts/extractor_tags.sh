#!/bin/sh
# Single source of truth for EXTRACTOR_VARIANT -> build tag mapping.
# Sourced by platform Taskfiles via: . "{{.ROOT_DIR}}/scripts/extractor_tags.sh" "$variant"
# Outputs the tag string (empty for generic, "extractor" for full-pack).
# Exits 1 on unknown variant (fail-closed).
v="${1:-}"
case "$v" in
  ""|"generic-no-pack") printf '' ;;
  "full-pack") printf 'extractor' ;;
  *) echo "unknown EXTRACTOR_VARIANT: $v" >&2; exit 1 ;;
esac
