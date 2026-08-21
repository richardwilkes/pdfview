#! /usr/bin/env bash

# Benchmarks the goexperiment.simd kernels against the portable scalar code they replace. For every package named on
# the command line (or the kernel packages listed below, when none are), this runs the benchmarks whose names contain
# "SIMD" twice — once in the default build, once with GOEXPERIMENT=simd — and hands the pair to benchstat.
#
# Both arms run the same benchmark bodies; the only difference is which implementation the build tags selected, so a
# benchstat delta is the kernel's contribution and nothing else. Results land in ./simd-bench-results.
#
# Usage: ./simd-bench.sh [package ...]

set -eo pipefail

trap 'echo -e "\033[33;5msimd-bench failed on simd-bench.sh:$LINENO\033[0m"' ERR

GOVER=$(go env GOVERSION)
case "$GOVER" in
  go1.2[7-9]* | go1.[3-9]* | go[2-9]*) ;;
  *)
    echo "Go 1.27 or later is required for GOEXPERIMENT=simd (found $GOVER)" >&2
    exit 1
    ;;
esac

PKGS=("$@")
if [ ${#PKGS[@]} -eq 0 ]; then
  PKGS=(./ ./internal/jbig2 ./internal/render ./internal/filter ./internal/imaging ./internal/jpeg2000/codestream \
    ./internal/jpeg2000/wavelet)
fi

COUNT=${COUNT:-10}
OUT=simd-bench-results
rm -rf "$OUT"
mkdir -p "$OUT"
echo '*' >"$OUT/.gitignore"

{
  date
  go version
  echo "GOOS=$(go env GOOS) GOARCH=$(go env GOARCH)"
  if [ "$(uname -s)" = "Darwin" ]; then
    sysctl -n machdep.cpu.brand_string 2>/dev/null || true
  else
    grep -m1 'model name' /proc/cpuinfo 2>/dev/null || true
  fi
  git rev-parse HEAD 2>/dev/null || true
} >"$OUT/sysinfo.txt"
cat "$OUT/sysinfo.txt"

# Locate benchstat if it is around. The raw files are the data of record either way.
if command -v benchstat >/dev/null 2>&1; then
  BS=benchstat
elif [ -x "$(go env GOPATH)/bin/benchstat" ]; then
  BS="$(go env GOPATH)/bin/benchstat"
else
  BS=""
  echo -e "\033[33mbenchstat not found; raw files only - or: go install golang.org/x/perf/cmd/benchstat@latest\033[0m"
fi

# run <output file> <package> [env prefix...] - a failure here is reported, not fatal: one package that will not build
# should not throw away the numbers from the rest.
run() {
  local file=$1 pkg=$2
  shift 2
  if ! "$@" go test "$pkg" -run XXX -bench 'SIMD' -count "$COUNT" >"$file" 2>&1; then
    echo -e "  \033[31mFAILED\033[0m (see $file)"
    return 1
  fi
  return 0
}

EMPTY=()
for pkg in "${PKGS[@]}"; do
  # ./ -> root, ./internal/jpeg2000/wavelet -> internal_jpeg2000_wavelet
  name=$(echo "$pkg" | sed -e 's|^\./||' -e 's|/$||' -e 's|/|_|g')
  [ -n "$name" ] || name=root
  echo -e "\033[33m== $pkg\033[0m"
  ok=1
  run "$OUT/${name}_default.txt" "$pkg" env || ok=0
  run "$OUT/${name}_simd.txt" "$pkg" env GOEXPERIMENT=simd || ok=0
  [ "$ok" = "1" ] || continue

  # No kernels yet in this package, or none whose benchmark names match. Say so and move on rather than handing
  # benchstat two files it cannot parse.
  if ! grep -qE '^Benchmark' "$OUT/${name}_default.txt" "$OUT/${name}_simd.txt"; then
    echo "  no SIMD benchmarks"
    EMPTY+=("$pkg")
    rm -f "$OUT/${name}_default.txt" "$OUT/${name}_simd.txt"
    continue
  fi

  if [ -n "$BS" ]; then
    "$BS" "$OUT/${name}_default.txt" "$OUT/${name}_simd.txt" >"$OUT/${name}_benchstat.txt" 2>&1 || true
    cat "$OUT/${name}_benchstat.txt"
  else
    echo "  wrote $OUT/${name}_default.txt and $OUT/${name}_simd.txt"
  fi
done

echo
if [ ${#EMPTY[@]} -ne 0 ]; then
  echo "No SIMD benchmarks in: ${EMPTY[*]}"
fi
echo "Results in $OUT"
