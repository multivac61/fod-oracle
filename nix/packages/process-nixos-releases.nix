{
  pkgs,
  perSystem,
  ...
}:
pkgs.writeShellApplication {
  name = "process-nixos-releases";
  runtimeInputs = with pkgs; [
    perSystem.self.default # The FOD oracle binary
    coreutils # For date, mkdir, etc.
    bash # For shell scripting
  ];
  text = ''
    # Script to process NixOS releases from newest to oldest
    DB_PATH=''${FOD_ORACLE_DB_PATH:-"/var/lib/fod-oracle/db/fods.db"}
    LOG_DIR=''${FOD_ORACLE_LOG_DIR:-"$(pwd)/release_logs"}
    mkdir -p "$LOG_DIR"

    # Process a release by commit hash and version
    process_release() {
      local commit="$1"
      local version="$2"
      local log_file="$LOG_DIR/''${commit}_process.log"

      echo "Processing NixOS $version (commit $commit)..."
      local start_time
      start_time="$(date +%s)"

      if FOD_ORACLE_DB_PATH="$DB_PATH" fod-oracle "$commit" > "$log_file" 2>&1; then
        local duration=$(($(date +%s) - start_time))
        local mins=$((duration / 60))
        local secs=$((duration % 60))
        echo "✅ Success! ($mins""m $secs""s)"
      else
        local duration=$(($(date +%s) - start_time))
        local mins=$((duration / 60))
        local secs=$((duration % 60))
        echo "❌ Failed! ($mins""m $secs""s)"
      fi

      echo "-----------------------------------------"
      sleep 3
    }

    echo "Starting FOD processing for all NixOS releases..."
    echo "Database: $DB_PATH"
    echo "Logs: $LOG_DIR"
    echo ""

    # Modern releases - newest to oldest
    process_release "aae12a743f75097dd3a60a8265978b995298babc" "24.11"
    process_release "5646423bfac84ec68dfc60f2a322e627ef0d6a95" "24.05"
    process_release "7c6e3666e2040fb64d43b209b84f65898ea3095d" "23.11"
    process_release "90d94ea32eed9991e2b8c6a761ccd8145935c57c" "23.05"
    process_release "bd15cafc53d0aecd90398dd3ffc83a908bceb734" "22.11"
    process_release "7a94fcdda304d143f9a40006c033d7e190311b54" "22.05"
    process_release "506445d88e183bce80e47fc612c710eb592045ed" "21.11"
    process_release "fefb0df7d2ab2e1cabde7312238026dcdc972441" "21.05"
    process_release "aea7242187f21a120fe73b5099c4167e12ec9aab" "20.09"
    process_release "ce342ae31d3d542f1243f01a36c46438261cbfb6" "20.03"
    process_release "0d99a63fd353198d13b1f6c17eb747a9b28f47c5" "19.09"
    process_release "62cec46a6a20c84f5f479eacb0b41f3a451d8e8b" "19.03"
    process_release "52bc0a35b9d813a2a5c56b4a50e871aac5d85996" "18.09"
    process_release "4f57714e03b9e7fdc165b3a4aa8bfa0463afc027" "18.03"
    process_release "b213a386f91c89adee14d135d695bc623d70e61a" "17.09"
    process_release "40e02267b9893677b3e69933a4f178a46e03c98e" "17.03"
    process_release "7d50dfaed5540495385b5c0feae90891d2d0e72b" "16.09"
    process_release "8a7ca8508189facd3c34011dd0cdd895f840eb2f" "16.03"
    process_release "69721a81d0dd010ca9a79f293e27f74634bcd2b1" "15.09"

    # Pre-modern releases
    process_release "6ed8a76ac64c88df0df3f01b536498983ad5ad23" "0.14"
    process_release "aee659e1e20d6571ef40d28740297554ecff6255" "0.13"
    process_release "9e3f729c35abe3999b414e1c2c859b1fd711ab75" "0.12"
    process_release "7eec5e69f8699d6a4808ac77c434369445d62d37" "0.11"
    process_release "129a56627f08b76d3a1b3b15709023843fb25bdc" "0.10"
    process_release "9f61e1b99f8d94a1a1a191b54b6740a8da707862" "0.9"
    process_release "187d0a22eb5fa59effaa57c5ff562fb9221acfa9" "0.8"
    process_release "e5a0166f127a19c7aaa606bb4419d62cb186267e" "0.7"
    process_release "2b6c83f1f5ea40a5556491ad2e34f491b7683bfc" "0.6"
    process_release "f5c7d72813805ebfc73c738194fa05e02176c8f8" "0.5"
    process_release "e6db9b1caa9acc6ebd1a1e219ec084f2209ad68f" "0.4"
    process_release "d7d58daff4b40405017b3a29147301c9bbff0769" "0.3"
    process_release "12e195f313c29ca10b5e6c08047230e72b6c13f9" "0.2"
    process_release "8a74e5e56fc1fcf0a65a3b064d9829b21d298a4c" "0.1"

    echo "All NixOS releases have been processed!"
    echo "Log files are available in $LOG_DIR"
  '';

  meta.description = "Process NixOS releases sequentially to build FOD database";
}
