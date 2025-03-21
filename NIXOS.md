# Running FOD Oracle on NixOS

This document describes how to deploy FOD Oracle on a NixOS system.

## Overview

The NixOS module provided in this repository sets up:

1. The FOD Oracle API server as a systemd service
2. Proper database storage and permissions

## Prerequisites

- A NixOS system
- (Optional) agenix or sops-nix for secret management

## Installation

### Step 1: Add the module to your NixOS configuration

Add the FOD Oracle module to your `configuration.nix`:

```nix
{ config, pkgs, ... }:

let
  # Import the FOD Oracle module - adjust the path as needed
  fod-oracle-module = import /path/to/nixos-module.nix;
in
{
  imports = [
    # ... your other imports
    fod-oracle-module
  ];

  # Rest of your configuration...
}
```

### Step 2: Configure the FOD Oracle service

Add the service configuration to your `configuration.nix`:

```nix
services.fod-oracle = {
  enable = true;
  port = 8080; # Default API port
  dbPath = "/var/lib/fod-oracle/db/fods.db";
};
```

### Step 3: Rebuild your NixOS configuration

Apply the changes:

```bash
sudo nixos-rebuild switch
```

## Usage

After deployment, the FOD Oracle service will be accessible at:

- `http://localhost:8080`

## Database Management

The FOD database is stored at the configured `dbPath` (default: `/var/lib/fod-oracle/db/fods.db`). Make sure to:

1. Create this database before starting the service (using the FOD Oracle CLI)
2. Set up proper backups for the database
3. Consider mounting the database directory on persistent storage

## Advanced Configuration

For advanced configurations, you can customize:

- System resource limits in the systemd service
- Database connection parameters

See the `nixos-module.nix` file for all available options.

## Troubleshooting

If you encounter issues:

1. Check the service status: `systemctl status fod-oracle-api.service`
2. View logs: `journalctl -u fod-oracle-api.service`

### Common Issues

1. **API connectivity issues**: Verify the API server is running and accessible
2. **Database issues**: Ensure the database path is correct and the directory exists