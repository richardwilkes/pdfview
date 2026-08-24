#! /usr/bin/env bash
set -eo pipefail

trap 'echo -e "\033[33;5mBuild failed on build.sh:$LINENO\033[0m"' ERR

for arg in "$@"
do
  case "$arg" in
    --all|-a) LINT=1; TEST=1; RACE=-race ;;
    --lint|-l) LINT=1 ;;
    --race|-r) TEST=1; RACE=-race ;;
    --test|-t) TEST=1 ;;
    --help|-h)
      echo "$0 [options]"
      echo "  -a, --all  Equivalent to --lint --test --race"
      echo "  -l, --lint Run the linters"
      echo "  -r, --race Run the tests with race-checking enabled"
      echo "  -t, --test Run the tests"
      echo "  -h, --help This help text"
      exit 0
      ;;
    *)
      echo "Invalid argument: $arg"
      exit 1
      ;;
  esac
done

echo -e "\033[33mBuilding...\033[0m"
go build -v ./...

# The SIMD kernels are behind //go:build goexperiment.simd, so the default build never compiles them. GOEXPERIMENT stays
# a per-command prefix, never exported: the oracle module's cgo lint at the end must not inherit it.
echo -e "\033[33mBuilding with goexperiment.simd...\033[0m"
GOEXPERIMENT=simd go build ./...

if [ "$TEST"x == "1x" ]; then
  if [ -n "$RACE" ]; then
    echo -e "\033[33mTesting with -race enabled...\033[0m"
  else
    echo -e "\033[33mTesting...\033[0m"
  fi
  go test $RACE ./...
  echo -e "\033[33mTesting with goexperiment.simd...\033[0m"
  GOEXPERIMENT=simd go test $RACE ./...
fi

if [ "$LINT"x == "1x" ]; then
  GOLANGCI_LINT_VERSION=$(curl --head -s https://github.com/golangci/golangci-lint/releases/latest | grep -i location: | sed 's/^.*v//' | tr -d '\r\n' )
  TOOLS_DIR=$(go env GOPATH)/bin
  if [ ! -e "$TOOLS_DIR/golangci-lint" ] || [ "$("$TOOLS_DIR/golangci-lint" version 2>&1 | awk '{ print $4 }' || true)x" != "${GOLANGCI_LINT_VERSION}x" ]; then
    echo -e "\033[33mInstalling version $GOLANGCI_LINT_VERSION of golangci-lint into $TOOLS_DIR...\033[0m"
    mkdir -p "$TOOLS_DIR"
    curl -sfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$TOOLS_DIR" v$GOLANGCI_LINT_VERSION
  fi
  echo -e "\033[33mLinting...\033[0m"
  "$TOOLS_DIR/golangci-lint" run ./...
  echo -e "\033[33mLinting with goexperiment.simd...\033[0m"
  GOEXPERIMENT=simd "$TOOLS_DIR/golangci-lint" run ./...
  # The oracle module wraps MuPDF through cgo, so its lint pass needs cgo: do not run this script with CGO_ENABLED=0.
  (cd oracle; "$TOOLS_DIR/golangci-lint" run -c ../.golangci.yml ./...)
fi
