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
  };
}
