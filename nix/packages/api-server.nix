{
  pkgs,
  flake,
  pname,
}:
pkgs.buildGoModule {
  inherit pname;
  version = "0.1.0";

  src = flake;

  vendorHash = "sha256-RsONSrgfiOUKgIoiQf9yXff1pYH2TFecr3zaotUuImw=";

  postBuild = ''
    go build -o api-server ./cmd/api
  '';

  installPhase = ''
    mkdir -p $out/bin
    install -Dm755 api-server $out/bin/api-server
  '';
}
