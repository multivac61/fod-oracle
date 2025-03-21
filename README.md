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
- **Frontend**: Web UI for exploring and visualizing FOD data

## Usage

### CLI

To scan a nixpkgs revision:

```bash
./fod-oracle <nixpkgs-revision>
```

For example:

```bash
./fod-oracle 12345abcde67890fedcba09876543210abcdef12
```

### API Server

To start the API server:

```bash
./run-api.sh
```

The API server listens on port 8080 by default.

### Frontend

To start the frontend development server:

```bash
./run-frontend.sh
```

The frontend is available at http://localhost:5173.

## API Endpoints

The following API endpoints are available:

- `GET /api/health` - Health check
- `GET /api/revisions` - List all nixpkgs revisions
- `GET /api/revisions/{id}` - Get details for a specific revision
- `GET /api/revision/{rev}` - Get details for a specific revision by git hash
- `GET /api/fods` - List FODs (with pagination)
- `GET /api/fods/{hash}` - Find FODs by hash
- `GET /api/stats` - Get database statistics
- `GET /api/compare` - Compare FODs between revisions

## NixOS Module

FOD Oracle includes a NixOS module that makes it easy to deploy the API server with Caddy for HTTPS and Cloudflare DNS integration.

### Basic Configuration

```nix
{
  imports = [ 
    # Path to the FOD Oracle module
    /path/to/fod-oracle/nix/modules/nixos/default.nix 
  ];

  services.fod-oracle = {
    enable = true;
    domain = "api.your-domain.com";
    cloudflareApiTokenFile = "/path/to/cloudflare-token";
    openFirewall = true;
  };
}
```

### Integration Testing

To ensure that the NixOS module works correctly, FOD Oracle includes an integration test that runs only on x86_64-linux systems:

```bash
# Run all checks including the integration test (x86_64-linux only)
nix flake check -L
```

The integration test creates a NixOS VM, deploys FOD Oracle with the module, and verifies that both the API server and Caddy reverse proxy are working correctly.

Note: The integration test only runs on x86_64-linux platforms and is automatically skipped on other platforms like macOS or aarch64-linux.

## Development

### Requirements

- Go 1.21 or later
- Node.js 16 or later
- Nix (for running nix-eval-jobs)
- Git

### Building from Source

```bash
# Build the CLI
go build -o fod-oracle main.go

# Build the API server
go build -o api-server cmd/api/main.go

# Build the frontend
cd frontend
npm install
npm run build
```
