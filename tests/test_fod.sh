#!/usr/bin/env bash

set -e

echo "Running FOD Oracle test..."

# Get a fetchurl derivation path (a known FOD)
FOD_DRV_PATH=$(nix-instantiate --expr 'let pkgs = import <nixpkgs> {}; in pkgs.fetchurl { url = "https://example.com/test.txt"; sha256 = "0iwz771g1d6sh75v76k2rqgbwkxgmqvq6q5k78qpyzw8k3g7drsc"; }')
echo "Fetchurl derivation path: $FOD_DRV_PATH"

# Create test directory
TEST_DIR=$(mktemp -d)
echo "Test directory: $TEST_DIR"

# Test first with the test_fod.go script
echo "Testing if $FOD_DRV_PATH is a FOD with test_fod.go..."
go run -tags=integration "$(dirname "$0")/test_fod_main.go" "$FOD_DRV_PATH"

# Test with each output format of the main program
echo -e "\nTesting with SQLite output:"
DB_PATH="$TEST_DIR/test.db"
FOD_ORACLE_DB_PATH="$DB_PATH" FOD_ORACLE_TEST_DRV_PATH="$FOD_DRV_PATH" go run "$(dirname "$0")/../main.go" -format=sqlite -output="$DB_PATH" test-revision

echo -e "\nChecking SQLite database:"
sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM fods WHERE drv_path = '$FOD_DRV_PATH'" | grep -q "1" &&
  echo "SUCCESS: Fetchurl correctly identified as a FOD" ||
  echo "FAILURE: Fetchurl not identified as a FOD"

echo "Hash details in SQLite:"
sqlite3 "$DB_PATH" "SELECT hash_algorithm, hash FROM fods WHERE drv_path = '$FOD_DRV_PATH'"

echo -e "\nTesting with CSV output:"
CSV_PATH="$TEST_DIR/test.csv"
FOD_ORACLE_TEST_DRV_PATH="$FOD_DRV_PATH" go run "$(dirname "$0")/../main.go" -format=csv -output="$CSV_PATH" test-revision

echo "CSV output contents:"
head -n 5 "$CSV_PATH"

echo -e "\nTesting with Parquet (JSON) output:"
PARQUET_PATH="$TEST_DIR/test.parquet"
FOD_ORACLE_TEST_DRV_PATH="$FOD_DRV_PATH" go run "$(dirname "$0")/../main.go" -format=parquet -output="$PARQUET_PATH" test-revision

echo "Parquet (JSON) output preview:"
head -n 20 "$PARQUET_PATH"

# Test with a direct Nix expression
echo -e "\nTesting with a Nix expression using -expr flag:"
NIX_EXPR='let pkgs = import <nixpkgs> {}; in pkgs.fetchurl { url = "https://example.com/test.txt"; sha256 = "0iwz771g1d6sh75v76k2rqgbwkxgmqvq6q5k78qpyzw8k3g7drsc"; }'
go run "$(dirname "$0")/../main.go" -format=csv -output="$TEST_DIR/expr-test.csv" -expr "$NIX_EXPR"

echo "Nix expression test CSV output:"
head -n 5 "$TEST_DIR/expr-test.csv"

# Cleanup
echo -e "\nCleaning up..."
rm -rf "$TEST_DIR"

echo "All tests completed successfully."
