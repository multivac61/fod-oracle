# FOD Oracle Tests

This directory contains tests for the FOD Oracle application. Testing is primarily done through Nix and NixOS VM-based testing.

## Running Tests with Nix

The preferred way to run tests is using Nix. This ensures all dependencies are properly handled.

```bash
# Build the package with tests
nix build
```

## Running Tests Directly

You can also run the tests directly with Go:

```bash
# Run all tests
go test ./tests/...

# Run specific test files
go test ./tests/db_test.go

# Run with verbose output
go test -v ./tests/...

# Run integration tests
go test -tags=integration ./tests/...

# Run the test script
./tests/run_tests.sh
```

## Test Categories

1. **Database Tests**: Tests for the database schema and CRUD operations
2. **Utility Tests**: Tests for utility functions like `getCPUCores`
3. **Metadata Tests**: Tests for evaluation metadata storage
4. **Nix-related Tests**: Tests for Nix-specific functions (require git to be installed)
5. **Output Format Tests**: Tests for the different output formats (CSV, JSON, Parquet)
6. **NixOS VM Tests**: Complete system tests that run in NixOS VMs

### Output Format Tests

The `test_all_formats.sh` script tests all three output formats (CSV, JSON, and Parquet) to ensure they work correctly. This is especially useful to verify that the Parquet writer is properly configured.

To run the test:

```bash
cd /path/to/fod-oracle
./tests/test_all_formats.sh
```

The test creates temporary files in each format and verifies that they are created correctly with the expected content.

## NixOS VM Testing

The NixOS VM tests create a full virtual machine environment with multiple nodes to test the FOD Oracle:

1. A server node running the FOD Oracle service
2. A client node for testing API access

These tests are defined in `nix/checks/integration-test.nix` and are automatically run as part of `nix build`.

## Writing New Tests

When writing new tests, please follow these guidelines:

1. Create a new test file with a descriptive name and `_test.go` suffix
2. Use the helper functions in existing test files to set up common test fixtures
3. Add integration tests to the integration_test.go file and use the `integration` build tag
4. Document any external dependencies required for your tests
5. Update the NixOS VM tests if you add new features that need to be tested in a complete system environment