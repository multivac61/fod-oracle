{ pkgs, flake, ... }:
with pkgs;
buildGoModule {
  pname = "fod-oracle";
  version = "0.0";
  src = flake;
  vendorHash = "sha256-LELvkZBeqcqoRjF7kjy+8lLBjghHTXo53N6VrDpH/XE=";

  nativeBuildInputs = [ makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/fod-oracle \
      --prefix PATH : ${
        lib.makeBinPath (
          with pkgs;
          [
            nix-eval-jobs
            neofetch
          ]
        )
      }
  '';
}
