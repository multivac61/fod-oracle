{ pkgs, flake, ... }:
with pkgs;
buildGoModule {
  pname = "fod-oracle";
  version = "0.0";
  src = flake;
  vendorHash = "sha256-Z+VxAWb9jnF3L+7e+3zBQJu3pOHaBw3CyPoOTcKiLHI=";

  nativeBuildInputs = [ makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/fod-oracle \
      --prefix PATH : ${lib.makeBinPath [ pkgs.nix-eval-jobs ]}
  '';
}
