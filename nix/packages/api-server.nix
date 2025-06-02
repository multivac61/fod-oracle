{
  pkgs,
  flake,
  pname,
}:
pkgs.buildGoModule {
  inherit pname;
  version = "0.1.0";

  src = flake;

  vendorHash = "sha256-svQ1hG30MjNjiYE+NqCaWeD5eX+Li4N9G3sUJsr4PF0=";

  postBuild = ''
    go build -o api-server ./cmd/api
  '';

  installPhase = ''
    mkdir -p $out/bin
    install -Dm755 api-server $out/bin/api-server
  '';
}
