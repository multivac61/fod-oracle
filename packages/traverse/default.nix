{ pkgs, ... }:
with pkgs;
buildGoModule {
  pname = "traverse";
  version = "0.0";

  src = ./.;
  vendorHash = "sha256-fLqIpY+jpaINIt2EY0Phsqw7tNAQJ34caEJPTAVcjM4=";
}
