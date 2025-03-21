{ pkgs, ... }:
pkgs.caddy.withPlugins {
  hash = "sha256-wSgBOXa+OnZkwIKebSmvOizxZee5Zg/g3Di7fEO4FsI=";
  plugins = [
    "github.com/WeidiDeng/caddy-cloudflare-ip@v0.0.0-20231130002422-f53b62aa13cb"
    "github.com/caddy-dns/cloudflare@v0.0.0-20250228175314-1fb64108d4de"
  ];
}
