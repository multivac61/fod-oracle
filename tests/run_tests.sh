#!/bin/bash
set -e

# Change to the root directory of the project
cd "$(dirname "$0")/.."

# Clean up any test artifacts
echo "Cleaning up any previous test artifacts..."
rm -rf ./test_tmp

# Create a directory for test artifacts
mkdir -p ./test_tmp

echo "Running tests..."
go test -v ./tests/...

echo "All tests completed successfully!"
