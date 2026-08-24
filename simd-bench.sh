#! /usr/bin/env bash

# Benchmarks the goexperiment.simd kernels against the scalar code they replace. For each package on the command line
# (default: the kernel packages below), this runs the benchmarks whose names contain "SIMD" twice, once in the default
# build and once with GOEXPERIMENT=simd, and hands the pair to benchstat. Both arms run the same benchmark bodies, so
# the benchstat delta is the kernel's contribution alone. Results land in ./simd-bench-results.
#
# Usage: ./simd-bench.sh [package ...]

set -eo pipefail

trap 'echo -e "\033[33;5msimd-bench failed on simd-bench.sh:$LINENO\033[0m"' ERR

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

# benchstat is optional; the raw files are the data of record.
if command -v benchstat >/dev/null 2>&1; then
  BS=benchstat
elif [ -x "$(go env GOPATH)/bin/benchstat" ]; then
  BS="$(go env GOPATH)/bin/benchstat"
else
  BS=""
  echo -e "\033[33mbenchstat not found; raw files only - or: go install golang.org/x/perf/cmd/benchstat@latest\033[0m"
fi

# run <output file> <package> [env prefix...]. A failure is reported, not fatal, so one package that does not build
# keeps the numbers from the rest.
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

  # No SIMD benchmarks in this package: skip it, because benchstat cannot parse empty files.
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

tar czf simd-bench-results.tgz "$OUT"
echo
echo "Done. Send back simd-bench-results.tgz (or the $OUT directory)."
