{ pkgs, ... }:
with pkgs;
buildGoModule {
  pname = "traverse";
  version = "0.0";
  src = ./.;
  vendorHash = "sha256-Z+VxAWb9jnF3L+7e+3zBQJu3pOHaBw3CyPoOTcKiLHI=";

  # Add runtime dependencies
  buildInputs = [ pkgs.nix-eval-jobs ];
}
