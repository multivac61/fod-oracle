{ pkgs, flake, ... }:
with pkgs;
let
  mainPackage = buildGoModule {
    pname = "fod-oracle";
    version = "0.0";
    src = flake;
    vendorHash = "sha256-LELvkZBeqcqoRjF7kjy+8lLBjghHTXo53N6VrDpH/XE=";

    # Add proper Go test execution
    doCheck = true;

    checkPhase = ''
      # Ensure we have the necessary tools for testing
      export PATH=$PATH:${
        lib.makeBinPath [
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

    nativeBuildInputs = [ makeWrapper ];
    postFixup = ''
      wrapProgram $out/bin/fod-oracle \
        --prefix PATH : ${
          lib.makeBinPath (
            with pkgs;
            [
              nix-eval-jobs
              neofetch
            ]
          )
        }
    '';
  };

  # Combine the package and tests into a single derivation
  finalPackage = pkgs.symlinkJoin {
    name = "fod-oracle-with-tests";
    paths = [ mainPackage ];

    # Generate a test script to run unit tests
    postBuild = ''
      mkdir -p $out/bin

      # Create a script to run the unit tests
      cat > $out/bin/run-fod-oracle-tests <<EOF
      #!/usr/bin/env bash
      set -e

      echo "Running FOD Oracle tests..."

      # Set up test environment
      TMPDIR=\$(mktemp -d)
      trap "rm -rf \$TMPDIR" EXIT

      # Set environment variables
      export FOD_ORACLE_DB_PATH=\$TMPDIR/fods.db
      export FOD_ORACLE_NUM_WORKERS=2

      # Run unit tests
      cd ${flake.outPath}
      ${pkgs.go}/bin/go test -v ./tests/...

      echo "All tests passed!"
      EOF

      chmod +x $out/bin/run-fod-oracle-tests
    '';
  };
in
finalPackage
