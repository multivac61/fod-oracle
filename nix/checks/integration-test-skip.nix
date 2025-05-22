{ pkgs, ... }:

# Create a dummy test that always passes when using --builders ''
pkgs.runCommand "fod-oracle-integration-test-skipped" {
  meta.timeout = 1;
} "echo \"Integration test skipped when using --builders ''\" > $out"
