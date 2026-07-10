#!/usr/bin/env bash
# Curate a small, committed sample of the shencore golden corpus from a full
# harvest. The full corpus (test/parity/corpus/) is gitignored and regenerated
# with `make record-corpus`; this sample (test/parity/corpus-sample/) is what CI
# replays for byte-stability without needing envtest.
#
# Selection is deterministic. Instance-type catalogs are large (~1MB each), so the
# sample caps the number of distinct catalogs it pulls in: solve records are taken
# in lexical order but skipped once they would introduce a catalog beyond the cap,
# which keeps the committed fixtures small while still exercising every record
# shape. Disrupt records carry no catalog and are taken up to the per-kind limit.
set -euo pipefail

SRC="${1:-test/parity/corpus}"
DST="${2:-test/parity/corpus-sample}"
N="${SAMPLE_PER_KIND:-40}"
MAX_CATALOGS="${MAX_CATALOGS:-6}"

if [ ! -d "$SRC" ]; then
  echo "source corpus $SRC not found; run 'make record-corpus' first" >&2
  exit 1
fi

rm -rf "$DST"
mkdir -p "$DST"

catalog_ref() { grep -oE 'catalog-ref\| "[0-9a-f]{64}"' "$1" | grep -oE '[0-9a-f]{64}' | head -1; }

# Track included catalog shas in a space-delimited string (bash 3.2 has no
# associative arrays).
included_catalogs=" "
n_catalogs=0

# Solve records: keep within both the per-kind limit and the catalog cap.
solve_kept=0
while IFS= read -r f; do
  [ -z "$f" ] && continue
  [ "$solve_kept" -ge "$N" ] && break
  sha="$(catalog_ref "$f" || true)"
  if [ -n "$sha" ] && [ "${included_catalogs#* $sha }" = "$included_catalogs" ]; then
    [ "$n_catalogs" -ge "$MAX_CATALOGS" ] && continue
    included_catalogs="${included_catalogs}${sha} "
    n_catalogs=$((n_catalogs + 1))
    cp "$SRC/catalog-$sha.sexpr" "$DST/"
  fi
  cp "$f" "$DST/"
  solve_kept=$((solve_kept + 1))
done < <(find "$SRC" -maxdepth 1 -name 'solve-*.sexpr' | sort)

# Disrupt records: no catalog dependency.
find "$SRC" -maxdepth 1 -name 'disrupt-*.sexpr' | sort | head -n "$N" | while IFS= read -r f; do
  cp "$f" "$DST/"
done

records=$(find "$DST" -name '*.sexpr' -not -name 'catalog-*' | wc -l | tr -d ' ')
catalogs=$(find "$DST" -name 'catalog-*.sexpr' | wc -l | tr -d ' ')
echo "sample: $records records + $catalogs catalogs, size: $(du -sh "$DST" | cut -f1)"
