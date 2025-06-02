{
  pkgs,
  pname,
  flake,
  src ? flake, # Default to the root of the project
}:
pkgs.buildGoModule {
  inherit src pname;
  version = "0.1.0";

  vendorHash = "sha256-wAG+bp7lwd7VGxPZ1Ii0GmFdTGec01cfBzhBwiKiaIQ=";

  postBuild = ''
    go build -o api-server ./cmd/api
  '';

  installPhase = ''
    mkdir -p $out/bin
    install -Dm755 api-server $out/bin/api-server
  '';
}
