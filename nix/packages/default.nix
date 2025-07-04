{
  pkgs,
  flake,
  src ? flake,
}:
let
  pname = "fod-oracle";
in
pkgs.buildGoModule {
  inherit src pname;
  version = "0.0";
  vendorHash = "sha256-wAG+bp7lwd7VGxPZ1Ii0GmFdTGec01cfBzhBwiKiaIQ=";

  # Add proper Go test execution
  doCheck = true;

  checkPhase = ''
    # Ensure we have the necessary tools for testing
    export PATH=$PATH:${
      pkgs.lib.makeBinPath [
        pkgs.git
        pkgs.sqlite
        pkgs.nix-eval-jobs
        pkgs.neofetch
      ]
    }

    # Create test environment
    mkdir -p $TMPDIR/test-db

    # Configure environment variables for tests
    export FOD_ORACLE_DB_PATH=$TMPDIR/test-db/fods.db
    export FOD_ORACLE_NUM_WORKERS=2

    # Run the basic unit tests
    echo "Running unit tests..."
    go test -v ./tests/...
  '';

  nativeBuildInputs = [ pkgs.makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/${pname} \
      --prefix PATH : ${
        pkgs.lib.makeBinPath (
          with pkgs;
          [
            nix-eval-jobs
            neofetch
          ]
        )
      }
  '';
}
