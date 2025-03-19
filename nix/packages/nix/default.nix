{ pkgs, ... }:
pkgs.nixVersions.latest.overrideAttrs (_: {
  prePatch = ''
    substituteInPlace src/nix-env/nix-env.cc \
      --replace 'settings.readOnlyMode = true' 'settings.readOnlyMode = false'
  '';
  patches = [ ./primops.patch ];
})
