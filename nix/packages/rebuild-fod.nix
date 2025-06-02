{
  pkgs,
  pname,
  flake,
  src ? flake, # Default to the root of the project
  ...
}:
pkgs.buildGoModule {
  inherit pname src;
  version = "0.1.0";

  # This is a simplified approach - in a real setup, we'd compute this correctly
  vendorHash = "sha256-wAG+bp7lwd7VGxPZ1Ii0GmFdTGec01cfBzhBwiKiaIQ=";

  # Only build the rebuild-fod program
  subPackages = [ "cmd/rebuild-fod" ];

  # Build-time dependencies
  nativeBuildInputs = [ pkgs.makeWrapper ];

  # Runtime dependencies
  buildInputs = [
    pkgs.nix
  ];

  # Make Nix available at runtime
  postFixup = ''
    wrapProgram $out/bin/rebuild-fod \
      --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.nix ]}
  '';

  meta = {
    description = "Rebuild and verify fixed-output derivations (FODs)";
    license = pkgs.lib.licenses.mit;
    mainProgram = "rebuild-fod";
  };
}
