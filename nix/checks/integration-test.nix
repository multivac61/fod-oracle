{ pkgs, flake, ... }:
# Only run this test on Linux systems
if pkgs.stdenv.isLinux then
  pkgs.testers.runNixOSTest {
    name = "fod-oracle-integration-test";

    nodes = {
      # API server node
      server =
        { config, pkgs, ... }:
        {
          imports = [
            flake.nixosModules.default
          ];

          services.fod-oracle = {
            enable = true;
            port = 8080;
            domain = "fod-oracle-test.example.com";

            # Use a mock token for testing
            cloudflareApiToken = "mock_token_for_testing";

            # Database in a temporary location
            dbPath = "/tmp/fod-oracle-test.db";

            # Open firewall for testing
            openFirewall = true;
          };

          # Override Caddy configuration for testing
          services.caddy.virtualHosts."fod-oracle-test.example.com" = {
            extraConfig = ''
              # Use self-signed certificate for testing
              tls internal

              # API reverse proxy
              reverse_proxy /* http://127.0.0.1:${toString config.services.fod-oracle.port}
            '';
          };

          # Add hosts entry for local testing
          networking.hosts = {
            "127.0.0.1" = [ "fod-oracle-test.example.com" ];
          };
        };

      # Client node for testing the API
      client =
        { pkgs, ... }:
        {
          # Tools for testing the API
          environment.systemPackages = with pkgs; [
            curl
            jq
          ];
        };
    };

    # The actual test script
    testScript = ''
      import json

      start_all()

      # Wait for the services to be ready
      server.wait_for_unit("fod-oracle-api.service")
      server.wait_for_unit("caddy.service")
      server.wait_for_open_port(8080)
      server.wait_for_open_port(80)
      server.wait_for_open_port(443)

      # Test the API directly on the server
      with subtest("API health check direct access"):
          result = server.succeed("curl -s http://localhost:8080/api/health")
          assert "ok" in result, f"Health check failed: {result}"

      # Test API through Caddy with HTTPS
      with subtest("API health check through Caddy/HTTPS"):
          result = server.succeed("curl -s -k https://fod-oracle-test.example.com/api/health")
          assert "ok" in result, f"Health check through Caddy failed: {result}"

      # Test API endpoints
      with subtest("API endpoints"):
          # Test revisions endpoint
          revisions = json.loads(server.succeed("curl -s http://localhost:8080/api/revisions"))
          print(f"Revisions response: {revisions}")
          
          # Test stats endpoint
          stats = json.loads(server.succeed("curl -s http://localhost:8080/api/stats"))
          print(f"Stats response: {stats}")

      # Test from client node
      with subtest("Client access to API"):
          client.wait_until_succeeds("curl -s http://server:8080/api/health | grep -q 'ok'")
          
          # Test HTTPS access from client
          client.succeed("curl -s -k https://server/api/health | grep -q 'ok'")
    '';
  }
else
  pkgs.runCommand "fod-oracle-integration-test-skipped" { } ''
    echo "Integration test only runs on x86_64-linux" > $out
  ''
