let
  pkgs = import /tmp/nixpkgs { };
in
{
  inherit (pkgs) hello;
}
