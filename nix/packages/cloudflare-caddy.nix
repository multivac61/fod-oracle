{ pkgs, ... }:
pkgs.stdenv.mkDerivation {
  pname = "caddy-with-cloudflare";
  version = pkgs.caddy.version;

  meta.mainProgram = "caddy";

  dontUnpack = true;

  nativeBuildInputs = with pkgs; [
    xcaddy
    go
    cacert
    git
  ];

  buildPhase = ''
    # Set up Go environment
    export GOCACHE=$TMPDIR/go-cache
    export GOPATH="$TMPDIR/go"
    export HOME=$TMPDIR

    ${pkgs.xcaddy}/bin/xcaddy build --with github.com/caddy-dns/cloudflare
  '';

  installPhase = ''
    mkdir -p $out/bin
    cp caddy $out/bin/
  '';
}
