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
- **JSON Lines Streaming**: Outputs FODs as streaming JSON Lines for real-time processing
- **FOD Rebuilding**: Verifies FOD integrity by rebuilding and comparing hashes
- **API**: RESTful API for programmatic access to FOD data

## Components

- **CLI**: Command-line tool for scanning nixpkgs revisions and populating the database
- **API Server**: Provides RESTful API access to the FOD data

## Usage

### CLI

FOD Oracle outputs all results as streaming JSON Lines to stdout, making it easy to process with tools like `jq` or pipe to other programs.

```bash
# Process a simple Nix expression (outputs JSON Lines)
./fod-oracle -expr "(import <nixpkgs> {}).hello"

# Process a specific nixpkgs revision  
./fod-oracle 1d250f4

# Reevaluate FODs by rebuilding them (includes rebuild status in JSON)
./fod-oracle -reevaluate -parallel=4 -build-delay=5 1d250f4

# Enable debug logging
./fod-oracle -debug -expr "(import <nixpkgs> {}).hello"

# Process and filter with jq
./fod-oracle -expr "(import <nixpkgs> {}).hello" | jq '.Hash'

# Save JSON Lines to file
./fod-oracle 1d250f4 > fods.jsonl
```

#### Output Formats

**Normal Mode**: Outputs basic FOD information as JSON Lines
```json
{"DrvPath":"/nix/store/...","OutputPath":"/nix/store/...","HashAlgorithm":"sha256","Hash":"..."}
```

**Reevaluate Mode**: Includes rebuild verification results
```json
{"DrvPath":"/nix/store/...","OutputPath":"/nix/store/...","HashAlgorithm":"sha256","Hash":"...","rebuild_status":"success","actual_hash":"...","hash_mismatch":false}
```

Scanning a complete nixpkgs revision takes around 10+ minutes on a 7950 AMD Ryzen 9 16-core CPU with 62GB RAM.

### Command-line Arguments

```
Usage: ./fod-oracle [options] <nixpkgs-revision> [<nixpkgs-revision2> ...]

Options:
  -debug
    	Enable debug logging to stderr
  -drv string
    	Derivation path for test mode
  -expr string
    	Process a Nix expression instead of a revision
  -help
    	Show help
  -parallel int
    	Number of parallel rebuild workers (default: 1, use higher values for testing)
  -reevaluate
    	Reevaluate FODs by rebuilding them and include rebuild status in output
  -build-delay int
    	Delay between builds in seconds (default 10)
  -test
    	Test mode - process a single derivation
  -workers int
    	Number of worker threads (default 1)
```

### Environment Variables

- `FOD_ORACLE_NUM_WORKERS` - Number of worker threads (default: 1)
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
