# Example configuration.nix for FOD Oracle API Server with NixOS and Caddy+Cloudflare
{
  config,
  pkgs,
  lib,
  ...
}:

let
  # Import the FOD Oracle module
  fod-oracle-module = import ./nixos-module.nix;
in
{
  imports = [
    # ... your other imports
    fod-oracle-module
  ];

  # Enable the FOD Oracle API service
  services.fod-oracle = {
    enable = true;
    port = 8080; # Default API port
    dbPath = "/var/lib/fod-oracle/db/fods.db";

    # Domain configuration
    domain = "api.fod-oracle.org"; # Change to your API domain

    # Cloudflare DNS API authentication (choose one method)
    # Option 1: Direct token in configuration (less secure)
    # cloudflareApiToken = "your-cloudflare-api-token";

    # Option 2: Token from a file (more secure)
    cloudflareApiTokenFile = "/run/secrets/cloudflare_token";

    # Open firewall for HTTP/HTTPS
    openFirewall = true;

    # Optional: Tailscale access
    exposeThroughTailscale = false; # Set to true if you want Tailscale access as well
    tailscaleTags = [ "tag:fod-oracle" ]; # Only needed if exposeThroughTailscale = true
  };

  # System packages
  environment.systemPackages = with pkgs; [
    # Tools for database management and debugging
    sqlite
    curl
    jq
  ];

  # If using Tailscale as a backup access method
  services.tailscale = lib.mkIf config.services.fod-oracle.exposeThroughTailscale {
    enable = true;
  };

  # Secrets management with agenix (recommended)
  # age.secrets.cloudflare_token = {
  #   file = ./secrets/cloudflare_token.age;
  #   path = "/run/secrets/cloudflare_token";
  #   owner = "caddy";
  #   group = "caddy";
  #   mode = "0400";
  # };

  # Networking settings
  networking = {
    # Ensure domain name resolves locally for testing
    hosts = {
      "127.0.0.1" = [ "api.fod-oracle.org" ];
    };

    # Open firewall
    firewall = {
      enable = true;
      allowedTCPPorts = [
        80
        443
      ];
      # Tailscale traffic (if using Tailscale)
      allowedUDPPorts = lib.mkIf config.services.fod-oracle.exposeThroughTailscale [ 41641 ];
    };
  };

  # System configuration values
  system.stateVersion = "23.11"; # Replace with your NixOS version
}

