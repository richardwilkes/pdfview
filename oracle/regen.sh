#! /usr/bin/env bash
# Regenerates every golden under ../testfiles/goldens from ../testfiles/corpus by running MuPDF — via the published
# cgo binding checked out at ../../pdf — as the behavioral oracle (see plan.md). This script is local/manual only:
# it needs cgo, a C toolchain, and that sibling checkout. CI never runs it; the corpus and the goldens it produces
# are committed, and regeneration diffs are reviewed at commit time. Output is deterministic for a given corpus,
# MuPDF build, and Go release, so running it twice must leave the working tree unchanged.
#
# This is also the single registry of per-corpus-file dump parameters (passwords for the encrypted variants, search
# needles). When adding a corpus file, add a dump line here and document the file's provenance in
# ../testfiles/corpus/README.md.
set -eo pipefail
cd "$(dirname "$0")"

trap 'echo -e "\033[33;5mRegeneration failed on regen.sh:$LINENO\033[0m"' ERR

CORPUS=../testfiles/corpus
GOLDENS=../testfiles/goldens

# Wipe all goldens first so a removed corpus file cannot leave an orphaned golden directory behind — except the
# hand-maintained per-file pixel gates (thresholds.json), which are not oracle output and must survive
# regeneration. A removed corpus file's stale thresholds.json would be restored into an otherwise-empty
# directory; regen diffs are reviewed at commit time, so delete such orphans by hand when retiring a file.
THRESH_STASH=$(mktemp -d)
if [ -d "$GOLDENS" ]; then
  (cd "$GOLDENS" && find . -name thresholds.json | tar -cf "$THRESH_STASH/thresholds.tar" -T - 2>/dev/null) || true
fi
rm -rf "$GOLDENS"

dump() {
  local name="$1"
  shift
  echo -e "\033[33m==> $name\033[0m"
  CGO_ENABLED=1 go run . dump -in "$CORPUS/$name.pdf" -out "$GOLDENS/$name" "$@"
}

dump glaive -search GURPS -search the -search 'of the' -search Glaive
dump internal-links
dump vectors
dump text-std14 -search Hello -search 'hello world' -search 'brown fox' -search QUICK -search 'Spaced words' -search 'Kerned Text'
dump text-type1 -search BCD -search 'Aé' -search DAC -search eBe
dump text-type0-cid2 -search '你佡世界' -search WXYZ
dump text-type0-cid0 -search PRS -search SQRP
dump text-type3
dump text-trmodes -search 'Filled zero' -search 'Stroked pen' -search 'Both layers' -search 'Ghost words' \
  -search CLIPPED -search FILLCLIP -search Risen -search Wide -search STROKECLIP
dump std14-styles -search handgrip -search boldface -search obliquely -search chiseled -search romanesque \
  -search duckweed -search italicize -search marbled -search typewriter -search keystroke -search slanting \
  -search flywheel -search 'αβγδ' -search '✁✂✃'
dump subst-metrics -search paxo -search qbxo -search rcxo -search sdxo -search texo -search ufxo \
  -search vgxo -search wexo -search xfxo -search ygxo -search zhxo -search aixo
dump hit-quad-split -search 'backup withholding' -search 'alpha beta'
dump rotate90 -search Rotated
dump damaged-startxref-zero -search Repaired
dump damaged-bad-offsets -search Repaired -search Second
dump damaged-no-trailer -search Repaired
dump encrypted-r2-rc4 -password user -password owner -search Hello
dump encrypted-r3-rc4 -password user -password owner -search Hello
dump encrypted-r4-rc4 -password user -password owner -search Hello
dump encrypted-r4-aes -password user -password owner -search Hello
dump encrypted-r6-aes -password user -password owner -search Hello
dump encrypted-r6-empty-user -password owner -search Hello
dump irs-f1040 -search 'Filing Status' -search Income
dump irs-fw9 -search taxpayer -search 'backup withholding'
dump images-dct
dump images-raw
dump images-indexed
dump images-imagemask
dump images-inline
dump images-smask
dump images-ccitt
dump images-jbig2
dump images-jpx
dump images-interpolate
dump shading-axial
dump shading-radial
dump shading-function
dump shading-mesh
dump pattern-tiling
dump transparency-blend
dump transparency-group
dump transparency-smask-lum
dump transparency-smask-alpha
dump annotations -search WIDGETXT -search INHERITX

if [ -s "$THRESH_STASH/thresholds.tar" ]; then
  tar -xf "$THRESH_STASH/thresholds.tar" -C "$GOLDENS"
fi
rm -rf "$THRESH_STASH"

echo -e "\033[32mDone.\033[0m"
