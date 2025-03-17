{
  pkgs,
  ...
}:
pkgs.writeShellApplication {
  name = "download-verify";
  runtimeInputs = with pkgs; [
    jq
    curl
    coreutils
    gnugrep
    gnused
    nix
    nix-prefetch-scripts
  ];
  text = ''
    # Function to extract URL from the JSON file
    extract_url() {
      local file="$1"
      jq -r '.[] | select(.env != null) | .env.urls' "$file"
    }

    # Function to extract SHA256 hash from the JSON file
    extract_hash() {
      local file="$1"
      # Extract the hash without the sha256- prefix
      jq -r '.[] | select(.env != null) | .env.outputHash' "$file" | sed 's/sha256-//'
    }

    # Function to convert base32 to base64 (compatible with older Nix)
    b32_to_b64() {
      local hash="$1"
      # Use nix-hash directly for conversion if available
      if command -v nix-hash >/dev/null 2>&1; then
        nix-hash --type sha256 --to-base64 "$hash"
      # Fall back to nix hash if nix-hash is not available
      elif nix hash --help 2>&1 | grep -q "to-base64"; then
        nix hash to-base64 --type sha256 "$hash"
      # If neither works, inform the user
      else
        echo "Error: Cannot convert hash format. Please install nix-hash or update Nix."
        exit 1
      fi
    }

    # Main script
    if [ "$#" -ne 1 ]; then
      echo "Usage: $0 <json_file>"
      exit 1
    fi

    JSON_FILE="$1"

    # Extract URL and hash
    URL=$(extract_url "$JSON_FILE")
    EXPECTED_HASH_B64=$(extract_hash "$JSON_FILE")

    if [ -z "$URL" ]; then
      echo "Error: Could not find URL in the JSON file"
      exit 1
    fi

    if [ -z "$EXPECTED_HASH_B64" ]; then
      echo "Error: Could not find SHA256 hash in the JSON file"
      exit 1
    fi

    echo "Found URL: $URL"
    echo "Expected hash (base64): $EXPECTED_HASH_B64"

    # Download the file
    TMPDIR=$(mktemp -d)
    pushd "$TMPDIR"
    echo "Downloading file..."
    ARCHIVE_NAME=$(basename "$URL")

    if [ -n "$GITHUB_TOKEN" ]; then
      if ! curl -L -H "Authorization: token $GITHUB_TOKEN" "$URL" -o "$ARCHIVE_NAME"; then
        echo "Error: Download failed"
        exit 1
      fi
    else
      if ! curl -L "$URL" -o "$ARCHIVE_NAME"; then
        echo "Error: Download failed"
        exit 1
      fi
    fi

    echo "Downloaded file saved as: $ARCHIVE_NAME"

    # Get the hash using nix-prefetch-url (the most reliable method)
    echo "Computing hash using nix-prefetch-url..."
    NIX_HASH_B32=$(nix-prefetch-url --unpack "file://$(pwd)/$ARCHIVE_NAME" 2>/dev/null)

    # Convert the base32 hash to base64 for comparison
    NIX_HASH_B64=$(b32_to_b64 "$NIX_HASH_B32")

    echo "Computed hash (base32): $NIX_HASH_B32"
    echo "Computed hash (base64): $NIX_HASH_B64"
    echo "Expected hash (base64): $EXPECTED_HASH_B64"

    # Compare hashes
    if [ "$NIX_HASH_B64" = "$EXPECTED_HASH_B64" ]; then
      echo "✅ Hash verification successful!"
    else
      echo "❌ Hash verification failed!"
      echo "This could be due to:"
      echo "1. The file has been modified since the hash was generated"
      echo "2. The hash in the JSON file is incorrect"
      echo "3. The hash format conversion is not working correctly"
      
      echo ""
      echo "If you want to update your Nix expression with the correct hash, use:"
      echo "sha256-$(echo "$NIX_HASH_B64" | tr -d '\n')"
    fi

    popd
  '';
}
