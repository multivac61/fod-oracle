#!/usr/bin/env bash

set -e

echo "Running FOD Oracle test for fetchurl..."

# Get fetchurl derivation path
FOD_DRV_PATH=$(nix-instantiate --expr 'let pkgs = import <nixpkgs> {}; in pkgs.fetchurl { url = "https://example.com/test.txt"; sha256 = "0iwz771g1d6sh75v76k2rqgbwkxgmqvq6q5k78qpyzw8k3g7drsc"; }')
FOD_OUT_PATH=$(nix-build --no-out-link --expr 'let pkgs = import <nixpkgs> {}; in pkgs.fetchurl { url = "https://example.com/test.txt"; sha256 = "0iwz771g1d6sh75v76k2rqgbwkxgmqvq6q5k78qpyzw8k3g7drsc"; }' || echo "Build failed but continuing")

echo "Fetchurl derivation path: $FOD_DRV_PATH"
echo "Fetchurl output path: $FOD_OUT_PATH"

# Create test directory
TEST_DIR=$(mktemp -d)
echo "Test directory: $TEST_DIR"

# Set up test database
DB_PATH="$TEST_DIR/test.db"
echo "Test database: $DB_PATH"

# Compile the program if needed
if [ ! -f "./fod-oracle" ]; then
  echo "Compiling fod-oracle..."
  go build -o fod-oracle
fi

# Run the program in test mode
echo "Running fod-oracle in test mode..."
FOD_ORACLE_DB_PATH="$DB_PATH" FOD_ORACLE_TEST_DRV_PATH="$FOD_DRV_PATH" ./fod-oracle test-fetchurl

# Check if the derivation was added to the database
echo "Checking database..."
sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM fods WHERE drv_path = '$FOD_DRV_PATH'" | grep -q "1" &&
  echo "SUCCESS: Fetchurl correctly identified as a FOD" ||
  echo "FAILURE: Fetchurl not identified as a FOD"

# Check if the hash and algorithm were properly extracted
echo "Hash details:"
sqlite3 "$DB_PATH" "SELECT hash_algorithm, hash FROM fods WHERE drv_path = '$FOD_DRV_PATH'"

# Cleanup
echo "Cleaning up..."
rm -rf "$TEST_DIR"

echo "Test completed."

