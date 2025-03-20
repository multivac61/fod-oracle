# Running FOD Oracle on NixOS with Caddy and Cloudflare DNS

This document describes how to deploy FOD Oracle on a NixOS system with Caddy webserver and Cloudflare DNS integration for secure public access at fod-oracle.org.

## Overview

The NixOS module provided in this repository sets up:

1. The FOD Oracle API server as a systemd service
2. The FOD Oracle frontend as a static site served by Caddy
3. HTTPS with automatic certificate management via Caddy + Cloudflare DNS
4. Optional Tailscale integration for alternative access
5. Proper database storage and permissions

## Prerequisites

- A NixOS system
- A domain name managed by Cloudflare (configured for fod-oracle.org by default)
- A Cloudflare API token with DNS editing permissions
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
  
  # Domain configuration
  domain = "fod-oracle.org"; # Change to your domain
  
  # Cloudflare API token (choose one method)
  # Option 1: Direct token (less secure)
  # cloudflareApiToken = "your-cloudflare-api-token";
  
  # Option 2: Token from file (more secure)
  cloudflareApiTokenFile = "/run/secrets/cloudflare_token";
  
  # Open firewall for HTTP/HTTPS
  openFirewall = true;
};
```

### Step 3: Set up your Cloudflare API token

1. Log in to your Cloudflare dashboard
2. Go to "My Profile" > "API Tokens"
3. Create a new token with permissions for "Zone:DNS:Edit" for your domain
4. Store this token securely

For better security, use agenix or sops-nix to manage your Cloudflare API token:

```nix
# With agenix
age.secrets.cloudflare_token = {
  file = ./secrets/cloudflare_token.age;
  path = "/run/secrets/cloudflare_token";
  owner = "caddy";
  group = "caddy";
  mode = "0400";
};

# Or with sops-nix
sops.secrets.cloudflare_token = {
  sopsFile = ./secrets/secrets.yaml;
  path = "/run/secrets/cloudflare_token";
  owner = "caddy";
  group = "caddy";
};
```

### Step 4: Configure DNS for your domain

In your Cloudflare dashboard:

1. Add an A record for your domain pointing to your server's IP address
2. Make sure the domain's DNS proxying is enabled (orange cloud icon)

### Step 5: Rebuild your NixOS configuration

Apply the changes:

```bash
sudo nixos-rebuild switch
```

## Usage

After deployment, the FOD Oracle service will be accessible at:

- `https://fod-oracle.org` (or your custom domain)
- Locally at `http://localhost` or `https://localhost`

## Database Management

The FOD database is stored at the configured `dbPath` (default: `/var/lib/fod-oracle/db/fods.db`). Make sure to:

1. Create this database before starting the service (using the FOD Oracle CLI)
2. Set up proper backups for the database
3. Consider mounting the database directory on persistent storage

## Cloudflare DNS Configuration

### Required API Permissions

The Cloudflare API token needs the following permissions:

- Zone - DNS - Edit
- Zone - Zone - Read

Restrict the token to only the zones (domains) that you need for this service.

### Testing the API Token

To test if your API token is working correctly:

```bash
curl -X GET "https://api.cloudflare.com/client/v4/user/tokens/verify" \
     -H "Authorization: Bearer YOUR_TOKEN" \
     -H "Content-Type: application/json"
```

## Optional: Tailscale Fallback Access

You can also enable Tailscale for alternative access:

```nix
services.fod-oracle = {
  # ... other settings
  
  # Enable Tailscale integration
  exposeThroughTailscale = true;
  tailscaleTags = ["tag:fod-oracle"];
};

services.tailscale = {
  enable = true;
  authKeyFile = config.sops.secrets.tailscale_key.path;
};
```

## Advanced Configuration

For advanced configurations, you can customize:

- Caddy settings for custom TLS parameters
- System resource limits in the systemd service
- Database connection parameters

See the `nixos-module.nix` file for all available options.

## Troubleshooting

If you encounter issues:

1. Check the service status: `systemctl status fod-oracle-api.service caddy.service`
2. View logs: `journalctl -u fod-oracle-api.service -u caddy.service`
3. Test Caddy config: `caddy validate --config /var/lib/caddy/config.json`
4. Check DNS records in Cloudflare dashboard
5. Verify Cloudflare API token permissions and expiration

### Common Issues

1. **TLS certificate errors**: Ensure your Cloudflare API token has DNS permissions
2. **Frontend not loading**: Check that the static files are correctly built and deployed
3. **API connectivity issues**: Verify API server is running and Caddy reverse proxy is configured correctly