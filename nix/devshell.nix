{ pkgs, perSystem }:
pkgs.mkShell {
  # Add build dependencies including Go for the simple FOD oracle
  packages = with pkgs; [
    nix-eval-jobs
    jq
    go # Add Go for building the simple FOD oracle
  ];

  # Add environment variables
  env = { };

  inputsFrom = [ perSystem.self.simple-fod-oracle ];

  # Load custom bash code
  shellHook = '''';
}
