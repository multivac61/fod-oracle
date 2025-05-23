#!/usr/bin/env bash

set -e

echo "Running test to find all FODs that hello depends on..."

# Compile and run the test
cd "$(dirname "$0")/.."
cd "$(dirname "$0")/.." && go run -tags=integration tests/hello_deps_main.go

echo "Test completed."
