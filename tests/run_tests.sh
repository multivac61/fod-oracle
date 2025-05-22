#!/usr/bin/env bash
set -e

# Change to the root directory of the project
cd "$(dirname "$0")/.."

# Clean up any test artifacts
echo "Cleaning up any previous test artifacts..."
rm -rf ./test_tmp

# Create a directory for test artifacts
mkdir -p ./test_tmp

# Track Go tests success
GO_TESTS_SUCCESS=0

# Run Go tests unless they're skipped
if [ "${SKIP_GO_TESTS:-}" != "1" ]; then
  echo "Running Go tests..."
  go test -v ./tests/...
  GO_TESTS_SUCCESS=$?
else
  echo "Skipping Go tests (SKIP_GO_TESTS=1)"
  # Consider skipped tests as successful
  GO_TESTS_SUCCESS=0
fi

# Track overall success
FORMAT_TESTS_SUCCESS=0

# Check if format tests should be skipped (e.g., if running in CI without dependencies)
if [ "${SKIP_FORMAT_TESTS:-}" != "1" ]; then
  echo "Running format output tests..."
  # Run with error handling
  if ./tests/format_test_runner.sh; then
    FORMAT_TESTS_SUCCESS=1
    echo "Format tests passed!"
  else
    echo "WARNING: Format tests failed, but continuing with other tests"
  fi
else
  echo "Skipping format output tests (SKIP_FORMAT_TESTS=1)"
  # Consider skipped tests as successful
  FORMAT_TESTS_SUCCESS=1
fi

# No need to get status here - it's set earlier

if [ "$GO_TESTS_SUCCESS" -eq 0 ] && [ "$FORMAT_TESTS_SUCCESS" -eq 1 ]; then
  echo "All tests completed successfully!"
else
  if [ "$GO_TESTS_SUCCESS" -ne 0 ]; then
    echo "Go tests failed. See output above for details."
  fi
  if [ "$FORMAT_TESTS_SUCCESS" -ne 1 ]; then
    echo "Format tests failed. See output above for details."
  fi
  exit 1
fi
