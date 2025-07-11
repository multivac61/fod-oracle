{
  pkgs,
  flake,
  src ? flake,
}:
let
  pname = "fod-oracle";
in
pkgs.buildGoModule {
  inherit src pname;
  version = "0.0";
  vendorHash = "sha256-IVi/XtfrhtQiBlFMiBoDPiuAttRivNeC7eoTe0+stV4=";

  # Skip tests for now
  doCheck = false;

  nativeBuildInputs = [ pkgs.makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/${pname} \
      --prefix PATH : ${
        pkgs.lib.makeBinPath (
          with pkgs;
          [
            nix-eval-jobs
            nix # For nix-hash and nix hash commands
          ]
        )
      }
  '';
}
