# NixOS module for FOD-Oracle API Server with Caddy and Cloudflare DNS integration
{
  config,
  lib,
  pkgs,
  ...
}:

with lib;

let
  cfg = config.services.fod-oracle;
in
{
  options.services.fod-oracle = {
    enable = mkEnableOption "FOD Oracle API service";

    port = mkOption {
      type = types.port;
      default = 8080;
      description = "Port on which the FOD Oracle API server will listen.";
    };

    dbPath = mkOption {
      type = types.path;
      default = "/var/lib/fod-oracle/db/fods.db";
      description = "Path to the FOD Oracle SQLite database.";
    };

    package = mkOption {
      type = types.package;
      default = pkgs.fod-oracle.api-server;
      defaultText = literalExpression "pkgs.fod-oracle.api-server";
      description = "The FOD Oracle API server package to use.";
    };

    user = mkOption {
      type = types.str;
      default = "fod-oracle";
      description = "User account under which FOD Oracle runs.";
    };

    group = mkOption {
      type = types.str;
      default = "fod-oracle";
      description = "Group under which FOD Oracle runs.";
    };

    openFirewall = mkOption {
      type = types.bool;
      default = false;
      description = "Whether to open the firewall for the FOD Oracle port.";
    };

    domain = mkOption {
      type = types.str;
      default = "fod-oracle.org";
      description = "Domain name to use for the FOD Oracle API service.";
    };

    cloudflareApiToken = mkOption {
      type = types.str;
      default = "";
      description = "Cloudflare API token for DNS challenges.";
    };

    cloudflareApiTokenFile = mkOption {
      type = types.str;
      default = "";
      description = "Path to file containing Cloudflare API token for DNS challenges.";
    };

    # Keep Tailscale options for alternative access
    exposeThroughTailscale = mkOption {
      type = types.bool;
      default = false;
      description = "Whether to expose the FOD Oracle API service through Tailscale.";
    };

    tailscaleTags = mkOption {
      type = types.listOf types.str;
      default = [ "tag:fod-oracle" ];
      description = "Tailscale tags for access control.";
    };
  };

  config = mkIf cfg.enable {
    # User and group
    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.group;
      description = "FOD Oracle service user";
      home = "/var/lib/fod-oracle";
      createHome = true;
    };

    users.groups.${cfg.group} = { };

    # Ensure the database directory exists and has correct permissions
    system.activationScripts.fod-oracle-dirs = {
      text = ''
        mkdir -p "$(dirname ${cfg.dbPath})"
        chown -R ${cfg.user}:${cfg.group} "$(dirname ${cfg.dbPath})"
      '';
      deps = [ ];
    };

    # API Server service
    systemd.services.fod-oracle-api = {
      description = "FOD Oracle API Server";
      wantedBy = [ "multi-user.target" ];
      after = [ "network.target" ];

      environment = {
        FOD_ORACLE_API_PORT = toString cfg.port;
        FOD_ORACLE_DB_PATH = cfg.dbPath;
      };

      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        Group = cfg.group;
        ExecStart = "${cfg.package}/bin/api-server";
        Restart = "on-failure";
        WorkingDirectory = "/var/lib/fod-oracle";
        StateDirectory = "fod-oracle";
        StateDirectoryMode = "0750";
      };
    };

    # Caddy configuration with Cloudflare DNS integration
    services.caddy = {
      enable = true;

      # Build Caddy with Cloudflare DNS module
      package = pkgs.caddy.withPlugins (plugins: [
        (plugins.buildPlugin {
          name = "cloudflare";
          src = pkgs.fetchFromGitHub {
            owner = "caddy-dns";
            repo = "cloudflare";
            rev = "v0.5.0"; # Use the most recent stable version
            # You'll need to replace this with the actual hash when you first build the module
            # If you try to build with this fake hash, Nix will fail but show you the correct hash to use
            sha256 = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
          };
          version = "v0.5.0";
        })
      ]);

      # Configure environment variables for Cloudflare DNS
      globalConfig = {
        admin = {
          disabled = false;
          config = {
            persist = true;
          };
        };
      };

      # Setup Caddy with Cloudflare DNS and your domain - API only
      virtualHosts = {
        ${cfg.domain} = {
          # Main domain configuration for API only
          extraConfig = ''
            # API reverse proxy
            reverse_proxy /* http://127.0.0.1:${toString cfg.port}

            # TLS with Cloudflare DNS challenge
            tls {
              dns cloudflare ${
                if cfg.cloudflareApiToken != "" then cfg.cloudflareApiToken else "{env.CLOUDFLARE_API_TOKEN}"
              }
            }
          '';
        };
      };
    };

    # Set environment variables for Cloudflare API token if file is provided
    systemd.services.caddy.environment = mkIf (cfg.cloudflareApiTokenFile != "") {
      CLOUDFLARE_API_TOKEN = "$(cat ${cfg.cloudflareApiTokenFile})";
    };

    # Tailscale integration (optional)
    services.tailscale = mkIf cfg.exposeThroughTailscale {
      enable = true;
      authKeyFile = mkDefault config.sops.secrets.tailscale_key.path;
      extraUpFlags = [
        "--advertise-tags=${concatStringsSep "," cfg.tailscaleTags}"
      ];
    };

    # Tailscale Funnel (optional)
    systemd.services.fod-oracle-tailscale-funnel = mkIf cfg.exposeThroughTailscale {
      description = "FOD Oracle Tailscale Funnel";
      wantedBy = [ "multi-user.target" ];
      after = [
        "tailscaled.service"
        "fod-oracle-api.service"
        "caddy.service"
      ];
      requires = [ "tailscaled.service" ];

      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${pkgs.tailscale}/bin/tailscale funnel 443";
        ExecStop = "${pkgs.tailscale}/bin/tailscale funnel off";
      };
    };

    # Open firewall for HTTP/HTTPS
    networking.firewall = mkIf cfg.openFirewall {
      allowedTCPPorts = [
        80
        443
      ];
    };
  };
}

