#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "Usage: $0 DRV_PATH"
  echo "Example: $0 /nix/store/y0jggj3ycygz74vn1a6bdynx7k0478fb-AMB-plugins-0.8.1.tar.bz2.drv"
  exit 1
fi

DRV_PATH="$1"

# --- Helper Function: Convert SRI/Base64 to Hex ---
# $1: Hash string (can be SRI, plain Base64, or already hex)
sri_to_hex() {
  local hash_input="$1"
  local base64_part
  local hex_output

  if [[ -z "$hash_input" || "$hash_input" == "null" ]]; then
    echo ""
    return
  fi

  # Check if it's already hex. Heuristic: no hyphens/colons, no base64 padding '=', only hex chars.
  # Common hex lengths: sha1=40, sha256=64, sha512=128. Check for >=32 hex chars.
  if ! [[ "$hash_input" == *[-:]* || "$hash_input" == *"="* || "$hash_input" == *"/"* || "$hash_input" == *"+"* ]] && [[ "$hash_input" =~ ^[0-9a-fA-F]{32,}$ ]]; then
    echo "$hash_input"
    return
  fi

  # Normalize SRI prefixes like sha256: to sha256-
  local normalized_sri="${hash_input}"
  normalized_sri="${normalized_sri/sha1:/sha1-}"
  normalized_sri="${normalized_sri/sha256:/sha256-}"
  normalized_sri="${normalized_sri/sha512:/sha512-}"

  if [[ "$normalized_sri" == "sha256-"* ]]; then
    base64_part="${normalized_sri#sha256-}"
  elif [[ "$normalized_sri" == "sha512-"* ]]; then
    base64_part="${normalized_sri#sha512-}"
  elif [[ "$normalized_sri" == "sha1-"* ]]; then
    base64_part="${normalized_sri#sha1-}"
  # If no algo prefix but looks like base64 (e.g., from a grep that only got the b64 part)
  elif [[ "$hash_input" == *"="* || "$hash_input" == *"/"* || "$hash_input" == *"+"* ]]; then
    base64_part="$hash_input"
  else
    # Not a recognized SRI, not plain base64. Could be malformed or something else.
    # echo "sri_to_hex: Unrecognized SRI format or not base64: $hash_input" >&2
    echo ""
    return
  fi

  if [[ -z "$base64_part" ]]; then
    # echo "sri_to_hex: Base64 part is empty for $hash_input" >&2
    echo ""
    return
  fi

  hex_output=$(echo "$base64_part" | base64 -d 2>/dev/null | xxd -p -c 256 2>/dev/null | tr -d '\n' || echo "")
  echo "$hex_output"
}
# --- End Helper Function ---

echo "🔄 Processing derivation: $DRV_PATH"

# Initialize variables to store results
HEX_HASH_M1=""
HEX_HASH_M2=""
HEX_HASH_M3=""
HEX_HASH_M4_EXPECTED=""
HEX_HASH_M4_ACTUAL=""
HEX_HASH_M4_COMPUTED=""

# Variables to track raw data for Method 4 build condition
RAW_DRV_OUTPUTHASH="" # From Method 1 (JSON) or Method 2 (nix-store)
DRV_OUTPUTPATH_M3=""  # From Method 3

# METHOD 1: Extract hash from nix show-derivation JSON output
echo "1️⃣ Getting hash from derivation JSON..."
DRV_JSON=$(nix show-derivation "$DRV_PATH" 2>/dev/null || echo "")
if [[ -n "$DRV_JSON" ]]; then
  # jq needs the exact key of the derivation in the JSON map
  M1_OUTPUT_HASH=$(echo "$DRV_JSON" | jq -r --arg key "$DRV_PATH" '.[$key].outputHash // empty' 2>/dev/null)
  M1_ALGO=$(echo "$DRV_JSON" | jq -r --arg key "$DRV_PATH" '.[$key].outputHashAlgo // "sha256"' 2>/dev/null)
  M1_MODE=$(echo "$DRV_JSON" | jq -r --arg key "$DRV_PATH" '.[$key].outputHashMode // "recursive"' 2>/dev/null)

  if [[ -n "$M1_OUTPUT_HASH" ]]; then
    RAW_DRV_OUTPUTHASH="$M1_OUTPUT_HASH"
    echo "💠 [Method 1] Raw hash from JSON: $M1_OUTPUT_HASH (Algo: $M1_ALGO, Mode: $M1_MODE)"

    CANDIDATE_HEX=""
    if [[ "$M1_MODE" == "flat" && "$M1_OUTPUT_HASH" =~ ^[0-9a-fA-F]{32,}$ ]]; then
      CANDIDATE_HEX="$M1_OUTPUT_HASH"
    elif [[ "$M1_OUTPUT_HASH" == "$M1_ALGO-"* && ("$M1_OUTPUT_HASH" == *"="* || "$M1_OUTPUT_HASH" == *"/"* || "$M1_OUTPUT_HASH" == *"+"*) ]]; then
      CANDIDATE_HEX=$(sri_to_hex "$M1_OUTPUT_HASH")
    else # Assume Nix base32 or other, try converting via nix-hash to SRI then to hex
      SRI_TEMP=$(nix-hash --to-sri "$M1_ALGO" "$M1_OUTPUT_HASH" 2>/dev/null || echo "")
      if [[ -n "$SRI_TEMP" ]]; then
        CANDIDATE_HEX=$(sri_to_hex "$SRI_TEMP")
      fi
    fi
    HEX_HASH_M1="$CANDIDATE_HEX"

    if [[ -n "$HEX_HASH_M1" ]]; then
      echo "💠 [Method 1] Hash (hex format): $HEX_HASH_M1"
    else
      echo "💠 [Method 1] Failed to convert raw hash to hex."
    fi
  else
    echo "💠 [Method 1] No 'outputHash' found in derivation JSON (normal for non-fixed-output derivations)."
  fi
else
  echo "💠 [Method 1] Failed to get derivation JSON."
fi

# METHOD 2: Direct query with nix-store -q command
echo "2️⃣ Getting hash using nix-store query..."
M2_OUTPUT_HASH=$(nix-store -q --binding outputHash "$DRV_PATH" 2>/dev/null || echo "")
if [[ -n "$M2_OUTPUT_HASH" ]]; then
  RAW_DRV_OUTPUTHASH="${RAW_DRV_OUTPUTHASH:-$M2_OUTPUT_HASH}"
  M2_ALGO=$(nix-store -q --binding outputHashAlgo "$DRV_PATH" 2>/dev/null || echo "sha256")
  M2_MODE=$(nix-store -q --binding outputHashMode "$DRV_PATH" 2>/dev/null || echo "recursive")

  echo "🔷 [Method 2] Raw hash from nix-store: $M2_OUTPUT_HASH (Algo: $M2_ALGO, Mode: $M2_MODE)"

  CANDIDATE_HEX=""
  if [[ "$M2_MODE" == "flat" && "$M2_OUTPUT_HASH" =~ ^[0-9a-fA-F]{32,}$ ]]; then
    CANDIDATE_HEX="$M2_OUTPUT_HASH"
  elif [[ "$M2_OUTPUT_HASH" == "$M2_ALGO-"* && ("$M2_OUTPUT_HASH" == *"="* || "$M2_OUTPUT_HASH" == *"/"* || "$M2_OUTPUT_HASH" == *"+"*) ]]; then
    CANDIDATE_HEX=$(sri_to_hex "$M2_OUTPUT_HASH")
  else
    SRI_TEMP=$(nix-hash --to-sri "$M2_ALGO" "$M2_OUTPUT_HASH" 2>/dev/null || echo "")
    if [[ -n "$SRI_TEMP" ]]; then
      CANDIDATE_HEX=$(sri_to_hex "$SRI_TEMP")
    fi
  fi
  HEX_HASH_M2="$CANDIDATE_HEX"

  if [[ -n "$HEX_HASH_M2" ]]; then
    echo "🔷 [Method 2] Hash (hex format): $HEX_HASH_M2"
  else
    echo "🔷 [Method 2] Failed to convert raw hash to hex."
  fi
else
  echo "🔷 [Method 2] No 'outputHash' binding found via nix-store (normal for non-fixed-output derivations)."
fi

# METHOD 3: Get output path and compute its hash directly
echo "3️⃣ Computing hash from output file..."
DRV_OUTPUTPATH_M3=$(nix-store -q --outputs "$DRV_PATH" 2>/dev/null | head -1 || echo "")

if [[ -n "$DRV_OUTPUTPATH_M3" ]]; then
  if [[ -e "$DRV_OUTPUTPATH_M3" ]]; then
    echo "📦 Output path: $DRV_OUTPUTPATH_M3"
    HEX_HASH_M3=$(nix-hash --type sha256 --flat "$DRV_OUTPUTPATH_M3" 2>/dev/null || echo "")
    if [[ -n "$HEX_HASH_M3" ]]; then
      echo "🟦 [Method 3] Hash (hex, computed from output): $HEX_HASH_M3"
    else
      echo "🟦 [Method 3] Failed to compute hash of $DRV_OUTPUTPATH_M3."
    fi
  else
    echo "📦 Output path $DRV_OUTPUTPATH_M3 does not exist in store. Build may be required."
  fi
else
  echo "🟦 [Method 3] Could not determine output path for $DRV_PATH."
fi

# METHOD 4: Only used if previous methods failed to provide initial info - try building
NEEDS_BUILD_M4=true
if [[ -n "${RAW_DRV_OUTPUTHASH:-}" || (-n "${DRV_OUTPUTPATH_M3:-}" && -e "${DRV_OUTPUTPATH_M3:-}") ]]; then
  NEEDS_BUILD_M4=false
  echo "4️⃣ Skipping build attempt as prior methods yielded fixed outputHash or an existing outputPath."
fi

if $NEEDS_BUILD_M4; then
  echo "4️⃣ No fixed outputHash or existing outputPath found yet, attempting to build $DRV_PATH..."
  BUILD_OUTPUT=$(nix-build --no-out-link "$DRV_PATH" 2>&1 || true)

  if echo "$BUILD_OUTPUT" | grep -q "hash mismatch"; then
    echo "⚠️ Hash mismatch detected during build attempt."

    SRI_EXPECTED_M4=$(echo "$BUILD_OUTPUT" | sed -n "s/.*wanted: .* hash '\([^']*\)'.*/\1/p" || echo "")
    SRI_ACTUAL_M4=$(echo "$BUILD_OUTPUT" | sed -n "s/.*got: .* hash '\([^']*\)'.*/\1/p" || echo "")

    if [[ -n "$SRI_EXPECTED_M4" ]]; then
      echo "⬛ [Method 4] Expected hash (SRI from error): $SRI_EXPECTED_M4"
      HEX_HASH_M4_EXPECTED=$(sri_to_hex "$SRI_EXPECTED_M4")
      [[ -n "$HEX_HASH_M4_EXPECTED" ]] && echo "⬛ [Method 4] Expected hash (hex format): $HEX_HASH_M4_EXPECTED"
    fi
    if [[ -n "$SRI_ACTUAL_M4" ]]; then
      echo "⬛ [Method 4] Actual hash (SRI from error): $SRI_ACTUAL_M4"
      HEX_HASH_M4_ACTUAL=$(sri_to_hex "$SRI_ACTUAL_M4")
      [[ -n "$HEX_HASH_M4_ACTUAL" ]] && echo "⬛ [Method 4] Actual hash (hex format): $HEX_HASH_M4_ACTUAL"
    fi
  elif BUILT_PATH_M4=$(echo "$BUILD_OUTPUT" | grep '^/nix/store/' | tail -n 1); then
    if [[ -n "$BUILT_PATH_M4" && -e "$BUILT_PATH_M4" ]]; then
      echo "✅ Build reported output path: $BUILT_PATH_M4"
      IS_DRV_OUTPUT=false
      for drv_out_path_check in $(nix-store -q --outputs "$DRV_PATH" 2>/dev/null); do
        if [[ "$drv_out_path_check" == "$BUILT_PATH_M4" ]]; then
          IS_DRV_OUTPUT=true
          break
        fi
      done

      if $IS_DRV_OUTPUT; then
        HEX_HASH_M4_COMPUTED=$(nix-hash --type sha256 --flat "$BUILT_PATH_M4" 2>/dev/null || echo "")
        if [[ -n "$HEX_HASH_M4_COMPUTED" ]]; then
          echo "⬛ [Method 4] Hash (hex, computed from built output): $HEX_HASH_M4_COMPUTED"
        else
          echo "⬛ [Method 4] Failed to compute hash of built output $BUILT_PATH_M4."
        fi
      else
        echo "⚠️ Build output path $BUILT_PATH_M4 does not match expected outputs of $DRV_PATH."
        echo "   Full build output for inspection: "
        # shellcheck disable=SC2001
        echo "$BUILD_OUTPUT" | sed 's/^/   | /'
      fi
    else
      echo "⚠️ Build seemed to produce a path ($BUILT_PATH_M4) but it's not accessible. Full build output:"
      # shellcheck disable=SC2001
      echo "$BUILD_OUTPUT" | sed 's/^/   | /'
    fi
  else
    echo "⚠️ Build attempt failed or produced no recognizable Nix store path. Full build output:"
    # shellcheck disable=SC2001
    echo "$BUILD_OUTPUT" | sed 's/^/   | /'
  fi
fi

# Summary of results
echo ""
echo "📊 SUMMARY OF HEX HASHES 📊"
echo "===================================="
FOUND_ANY_HEX_HASH=false
[[ -n "${HEX_HASH_M1:-}" ]] && {
  echo "Method 1 (JSON):          ${HEX_HASH_M1}"
  FOUND_ANY_HEX_HASH=true
}
[[ -n "${HEX_HASH_M2:-}" ]] && {
  echo "Method 2 (Query):         ${HEX_HASH_M2}"
  FOUND_ANY_HEX_HASH=true
}
[[ -n "${HEX_HASH_M3:-}" ]] && {
  echo "Method 3 (Computed):      ${HEX_HASH_M3}"
  FOUND_ANY_HEX_HASH=true
}
[[ -n "${HEX_HASH_M4_EXPECTED:-}" ]] && {
  echo "Method 4 (Build Wanted):  ${HEX_HASH_M4_EXPECTED}"
  FOUND_ANY_HEX_HASH=true
}
[[ -n "${HEX_HASH_M4_ACTUAL:-}" ]] && {
  echo "Method 4 (Build Got):     ${HEX_HASH_M4_ACTUAL}"
  FOUND_ANY_HEX_HASH=true
}
[[ -n "${HEX_HASH_M4_COMPUTED:-}" ]] && {
  echo "Method 4 (Build Computed):${HEX_HASH_M4_COMPUTED}"
  FOUND_ANY_HEX_HASH=true
}

if ! $FOUND_ANY_HEX_HASH; then
  echo "No hex hash could be determined through any method for $DRV_PATH."
fi

