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

To scan a nixpkgs revision:

```bash
nix run github:multivac61/fod-oracle -- <nixpkgs-revision>
```

This took around 7 minutes on a 7950 AMD Ryzen 9 16-core processor.

## API Endpoints

The following API endpoints are available:

- `GET [api.fod-oracle.org/health](https://api.fod-oracle.org/health)` - Health check
- `GET [api.fod-oracle.org/revisions](https://api.fod-oracle.org/revisions)` - List all nixpkgs revisions
- `GET api.fod-oracle.org/revisions/{id}` - Get details for a specific revision
- `GET api.fod-oracle.org/revision/{rev}` - Get details for a specific revision by git hash
- `GET [api.fod-oracle.org/fods](https://api.fod-oracle.org/fods)` - List FODs (with pagination)
- `GET api.fod-oracle.org/fods/{hash}` - Find FODs by hash
- `GET api.fod-oracle.org/commit/{commit}/fods` - List all FODs associated with a specific nixpkgs commit hash (with pagination)
- `GET [api.fod-oracle.org/stats](https://api.fod-oracle.org/stats)` - Get database statistics
