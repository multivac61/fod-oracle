{
  pkgs,
  pname,
  ...
}:
pkgs.writeShellApplication {
  name = pname;
  runtimeInputs = [
    pkgs.nix
    pkgs.jq
  ];
  text = builtins.readFile ./rebuild_fod.sh;
}
