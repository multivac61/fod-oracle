{ pkgs, ... }:
with pkgs;
buildGoModule {
  pname = "fod-vela";
  version = "0.0";
  src = ./.;
  vendorHash = "sha256-Z+VxAWb9jnF3L+7e+3zBQJu3pOHaBw3CyPoOTcKiLHI=";

  # Add nix-eval-jobs as a runtime dependency
  nativeBuildInputs = [ makeWrapper ];

  # Wrap the binary to ensure nix-eval-jobs is in PATH
  postFixup = ''
    wrapProgram $out/bin/fod-vela \
      --prefix PATH : ${lib.makeBinPath [ pkgs.nix-eval-jobs ]}
  '';
}
