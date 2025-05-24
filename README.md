<div align="center">

# fod-oracle

  <img src="./docs/sibyl.webp" height="150"/>

**Watching over [nixpkgs](https://github.com/NixOS/nixpkgs) for FOD discrepancies**

<p>
<img alt="Static Badge" src="https://img.shields.io/badge/Status-experimental-orange">
</p>

</div>

> Temet Nosce

## Overview

FOD Oracle is a tool for tracking and analyzing fixed-output derivations (FODs) across different revisions of nixpkgs. It helps identify discrepancies and changes in FODs that might indicate issues with build reproducibility.

## Features

- **FOD Tracking**: Scans nixpkgs revisions and tracks all fixed-output derivations
- **Comparison Tools**: Compare FODs between different nixpkgs revisions
- **API**: RESTful API for programmatic access to FOD data
- **Web UI**: User-friendly interface for exploring and comparing FOD data

## Components

- **CLI**: Command-line tool for scanning nixpkgs revisions and populating the database
- **API Server**: Provides RESTful API access to the FOD data

## Usage

### CLI

```bash
# Process a simple Nix expression
./fod-oracle -expr "(import <nixpkgs> {}).hello"

# Process a specific nixpkgs revision
./fod-oracle 1d250f4

# Reevaluate FODs by rebuilding them (with parallel workers)
./fod-oracle -reevaluate -parallel=4 -build-delay=5 1d250f4

# Output to JSON format
./fod-oracle -format=json -output=./output.json -expr "hello"

# Output to CSV format
./fod-oracle -format=csv -output=./output.csv -expr "hello"

# Output to Parquet format
./fod-oracle -format=parquet -output=./output.parquet -expr "hello"
```

Scanning a complete nixpkgs revision takes around 10+ minutes on a 7950 AMD Ryzen 9 16-core CPU with 62GB RAM.

### Command-line Arguments

```
Usage: ./fod-oracle [options] <nixpkgs-revision> [<nixpkgs-revision2> ...]

Options:
  -drv string
    	Derivation path for test mode
  -expr
    	Process a Nix expression instead of a revision
  -format string
    	Output format (sqlite, json, csv, parquet) (default "sqlite")
  -help
    	Show help
  -output string
    	Output path for non-SQLite formats
  -parallel int
    	Number of parallel rebuild workers (default: 1, use higher values for testing)
  -reevaluate
    	Reevaluate FODs by rebuilding them
  -build-delay int
    	Delay between builds in seconds (default 10)
  -test
    	Test mode - process a single derivation
  -workers int
    	Number of worker threads (default 1)
```

### Environment Variables

- `FOD_ORACLE_NUM_WORKERS` - Number of worker threads (default: 1)
- `FOD_ORACLE_DB_PATH` - Path to SQLite database (default: ./db/fods.db)
- `FOD_ORACLE_OUTPUT_FORMAT` - Output format (default: sqlite)
- `FOD_ORACLE_OUTPUT_PATH` - Output path for non-SQLite formats
- `FOD_ORACLE_TEST_DRV_PATH` - Path to derivation for test mode
- `FOD_ORACLE_EVAL_OPTS` - Additional options for nix-eval-jobs
- `FOD_ORACLE_BUILD_DELAY` - Delay between builds in seconds (default: 0)

## Rebuild-FOD Tool

The project includes a standalone `rebuild-fod` tool that can be used to rebuild and verify fixed-output derivations. This tool is built in Go and can be used both as a command-line utility and as a library in the main application.

### Building and Using the Tool with Nix

```bash
nix build .#rebuild-fod -- /nix/store/0m4y3j4pnivlhhpr5yqdvlly86p93fwc-busybox.drv
```

The rebuild-fod tool uses multiple methods to determine the correct hash of a fixed-output derivation:

1. Extracting from derivation JSON (Method 1)
2. Querying the Nix store (Method 2)
3. Computing from the output (Method 3)
4. Building the derivation if needed (Method 4)

It then compares the results to find any hash mismatches, which could indicate reproducibility issues.

## API Endpoints

The following API endpoints are available:

- https://api.fod-oracle.org/health - Health check
- https://api.fod-oracle.org/revisions - List all nixpkgs revisions
- https://api.fod-oracle.org/stats - Get database statistics
- https://api.fod-oracle.org/fods - List FODs (with pagination)
- `https://api.fod-oracle.org/revisions/{id}` - Get details for a specific revision
- `https://api.fod-oracle.org/revision/{rev}` - Get details for a specific revision by git hash
- `https://api.fod-oracle.org/fods/{hash}` - Find FODs by hash
- `https://api.fod-oracle.org/commit/{commit}/fods` - List all FODs associated with a specific nixpkgs commit hash (with pagination)
