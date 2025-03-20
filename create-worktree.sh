#!/bin/bash

# Script to create a Git worktree for a specific revision of nixpkgs
# Usage: ./create-worktree.sh <commit-hash>

set -e # Exit immediately if a command exits with a non-zero status
set -x # Print commands and their arguments as they are executed

# Get the revision from command line argument
REV=$1

if [ -z "$REV" ]; then
  echo "Usage: $0 <commit-hash>"
  echo "Example: $0 c755bd658ed0085d762ade083b0f5c3e9045cf18"
  exit 1
fi

# Validate hash format (should be 40 hex characters)
if ! [[ $REV =~ ^[0-9a-f]{40}$ ]]; then
  echo "Error: Invalid commit hash format. Expected 40 hexadecimal characters."
  exit 1
fi

# Define directories with absolute paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$SCRIPT_DIR/nixpkgs-repo"
WORKTREE_DIR="$SCRIPT_DIR/nixpkgs-worktree-$REV"
REPO_URL="https://github.com/NixOS/nixpkgs.git"

echo "Creating worktree for revision $REV"
echo "Repository directory: $REPO_DIR"
echo "Worktree directory: $WORKTREE_DIR"

# Step 1: COMPLETELY clean up any existing worktree with this name
if [ -d "$WORKTREE_DIR" ]; then
  echo "Directory $WORKTREE_DIR already exists"

  # Check if Git knows about this worktree
  if [ -d "$REPO_DIR" ] && git -C "$REPO_DIR" worktree list | grep -q "$WORKTREE_DIR"; then
    echo "Removing worktree from Git's perspective..."
    git -C "$REPO_DIR" worktree remove --force "$WORKTREE_DIR" || true
  fi

  # Regardless of whether Git knew about it, remove the directory
  echo "Forcibly removing directory..."
  rm -rf "$WORKTREE_DIR"

  # Verify it's gone
  if [ -d "$WORKTREE_DIR" ]; then
    echo "ERROR: Failed to remove directory $WORKTREE_DIR"
    exit 1
  fi

  echo "Successfully removed existing worktree directory"
fi

# Step 2: Check if the main repository exists
if [ ! -d "$REPO_DIR" ]; then
  echo "Creating bare clone of nixpkgs repository..."
  git clone --bare --filter=blob:none "$REPO_URL" "$REPO_DIR"
else
  echo "Using existing repository at $REPO_DIR"

  # Prune any stale worktrees
  echo "Pruning stale worktrees..."
  git -C "$REPO_DIR" worktree prune

  # List current worktrees
  echo "Current worktrees:"
  git -C "$REPO_DIR" worktree list
fi

# Step 3: Fetch the specific revision
echo "Fetching revision $REV..."
git -C "$REPO_DIR" fetch --depth=1 origin "$REV" || {
  echo "Retrying fetch without depth limit..."
  git -C "$REPO_DIR" fetch origin "$REV"
}

# Step 4: Create the worktree
echo "Creating worktree at $WORKTREE_DIR..."

# Double-check the directory doesn't exist
if [ -d "$WORKTREE_DIR" ]; then
  echo "ERROR: Directory $WORKTREE_DIR still exists despite cleanup"
  exit 1
fi

# Create the worktree with explicit path
git -C "$REPO_DIR" worktree add --detach "$WORKTREE_DIR" "$REV"

# Immediately check if the directory exists
echo "Checking if worktree directory was created..."
ls -la "$SCRIPT_DIR"

# Step 5: Verify the worktree was created correctly
if [ ! -d "$WORKTREE_DIR" ]; then
  echo "ERROR: Worktree directory was not created"

  # Check what Git thinks about the worktrees
  echo "Git worktree list:"
  git -C "$REPO_DIR" worktree list

  # Try to find the worktree in a different location
  echo "Searching for worktree directory..."
  find "$SCRIPT_DIR" -type d -name "nixpkgs-worktree-*" -print

  exit 1
fi

# Step 6: Verify critical files exist
echo "Verifying critical files..."
CRITICAL_FILES=(
  "default.nix"
  "pkgs/top-level/all-packages.nix"
  "lib/default.nix"
)

MAX_RETRIES=10
RETRY_DELAY=1

for ((i = 1; i <= MAX_RETRIES; i++)); do
  ALL_FILES_EXIST=true

  for FILE in "${CRITICAL_FILES[@]}"; do
    if [ ! -f "$WORKTREE_DIR/$FILE" ]; then
      ALL_FILES_EXIST=false
      echo "Waiting for file $FILE to be available (attempt $i/$MAX_RETRIES)"
      break
    fi
  done

  if $ALL_FILES_EXIST; then
    echo "Worktree is fully populated after $i attempts"
    break
  fi

  if [ $i -eq $MAX_RETRIES ]; then
    echo "ERROR: Worktree appears incomplete after $MAX_RETRIES attempts"
    echo "Directory contents:"
    ls -la "$WORKTREE_DIR"
    exit 1
  fi

  echo "Waiting for worktree to be fully populated (attempt $i/$MAX_RETRIES)..."
  sleep $RETRY_DELAY
done

# Get absolute path
ABSOLUTE_PATH="$WORKTREE_DIR"

echo "Success! Worktree created at: $ABSOLUTE_PATH"
echo "You can use this directory with nix-eval-jobs:"
echo "nix-eval-jobs --expr \"import $ABSOLUTE_PATH { allowAliases = false; }\" --workers 8"

# List all worktrees to see what Git knows about
echo "Current worktrees:"
git -C "$REPO_DIR" worktree list
