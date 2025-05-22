{ ... }:

let
  # Create a utility function to determine if we should skip tests
  # This is a simple heuristic that assumes --builders '' flag
  # is provided for local builds (no remote builders)
  shouldSkipVMTests =
    let
      # Check if NIX_CONFIG environment variable contains builders=''
      nixConfig = builtins.getEnv "NIX_CONFIG";
      hasEmptyBuilders =
        nixConfig != "" && builtins.match ".*builders[[:space:]]*=[[:space:]]*''.*" nixConfig != null;

      # Also check for the command-line flag --builders ''
      cmdLine = builtins.getEnv "NIX_OPERATION_ARGS";
      hasBuildersFlag =
        cmdLine != "" && builtins.match ".*--builders[[:space:]]+''.*" (" " + cmdLine + " ") != null;
    in
    hasEmptyBuilders || hasBuildersFlag;
in

# Override checks to skip VM tests when appropriate
{
  # Import the skip-test for integration test when using --builders ''
  integration-test =
    if shouldSkipVMTests then import ./integration-test-skip.nix else import ./integration-test.nix;
}
