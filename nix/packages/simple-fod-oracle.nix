{
  pkgs,
  pname,
  flake,
  src ? flake,
  ...
}:
pkgs.buildGoModule {
  inherit src pname;
  version = "1.0.0";
  vendorHash = "sha256-wAG+bp7lwd7VGxPZ1Ii0GmFdTGec01cfBzhBwiKiaIQ=";

  # Only build the simple-fod-oracle command
  subPackages = [ "cmd/simple-fod-oracle" ];

  # Build-time dependencies
  nativeBuildInputs = [ pkgs.makeWrapper ];

  # Runtime dependencies - minimal since we only process .drv files
  buildInputs = [
    pkgs.nix
  ];

  # Make Nix available at runtime for reading derivation files
  postFixup = ''
    wrapProgram $out/bin/simple-fod-oracle \
      --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.nix ]}
  '';

  meta = {
    description = "Simplified FOD oracle that processes JSON input with .drv paths";
    license = pkgs.lib.licenses.mit;
    mainProgram = "simple-fod-oracle";
  };
}
