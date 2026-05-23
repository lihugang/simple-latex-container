#!/usr/bin/env bash

set -euo pipefail

# This script builds the service binary from the repository root.
# It also runs the test suite first so the generated binary is only produced
# when the current source tree is in a passing state.

scriptDirectory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
binaryOutputPath="${scriptDirectory}/simple-latex-container"

# The default Go build cache location may be read-only in restricted
# environments, so the script places the cache under /tmp unless the caller
# explicitly overrides GOCACHE.
export GOCACHE="${GOCACHE:-/tmp/simple-latex-go-cache}"
mkdir -p "${GOCACHE}"

cd "${scriptDirectory}"

echo "Using GOCACHE=${GOCACHE}"
echo "Running tests..."
go test ./...

echo "Building ${binaryOutputPath}..."
go build -o "${binaryOutputPath}" .

echo "Build completed: ${binaryOutputPath}"
