{ flake, inputs }:
{
  config,
  pkgs,
  lib,
  ...
}:
let
  cfg = config.services.fod-oracle;
  pkgs' = flake.packages.${pkgs.system};
in
with lib;
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
      default = pkgs'.api-server;
      defaultText = literalExpression "pkgs.fod-oracle.api-server";
      description = "The FOD Oracle API server package to use.";
    };

    caddyPackage = mkOption {
      type = types.package;
      default = pkgs'.cloudflare-caddy;
      defaultText = literalExpression "pkgs.cloudflare-caddy or pkgs.caddy";
      description = "The Caddy package to use (with Cloudflare plugin).";
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
      description = "Path to file containing Cloudflare API token for DNS challenges. File should contain CLOUDFLARE_API_TOKEN=secret.";
    };

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

    tailscaleAuthKey = mkOption {
      type = types.str;
      default = "";
      description = "Path to file containing Tailscale auth key.";
    };
  };

  config = mkIf cfg.enable {
    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.group;
      description = "FOD Oracle service user";
      home = "/var/lib/fod-oracle";
      createHome = true;
    };

    users.groups.${cfg.group} = { };

    system.activationScripts.fod-oracle-dirs = {
      text = ''
        mkdir -p "$(dirname ${cfg.dbPath})"
        chown -R ${cfg.user}:${cfg.group} "$(dirname ${cfg.dbPath})"
      '';
      deps = [ ];
    };

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

    services.caddy = {
      enable = true;

      package = cfg.caddyPackage;

      virtualHosts = {
        ${cfg.domain} = {
          extraConfig = ''
            # API reverse proxy
            reverse_proxy /* http://127.0.0.1:${toString cfg.port}

            # TLS with Cloudflare DNS challenge
            tls {
              dns cloudflare {$CLOUDFLARE_API_TOKEN}
            }
          '';
        };
      };
    };
    systemd.services.caddy.serviceConfig.EnvironmentFile = cfg.cloudflareApiTokenFile;

    services.tailscale = mkIf cfg.exposeThroughTailscale {
      enable = true;
      authKeyFile = if cfg.tailscaleAuthKey != "" then cfg.tailscaleAuthKey else "{env.TS_AUTHKEY}";
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
