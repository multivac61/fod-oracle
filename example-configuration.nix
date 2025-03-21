# Example configuration.nix for FOD Oracle API Server with NixOS
{
  pkgs,
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
  };

  # System packages
  environment.systemPackages = with pkgs; [
    # Tools for database management and debugging
    sqlite
    curl
    jq
  ];

  # Networking settings
  networking = {
    # Ensure domain name resolves locally for testing
    hosts = {
      "127.0.0.1" = [ "localhost" ];
    };

    # Open firewall
    firewall = {
      enable = true;
      allowedTCPPorts = [
        8080 # FOD Oracle API port
      ];
    };
  };

  # System configuration values
  system.stateVersion = "23.11"; # Replace with your NixOS version
}

