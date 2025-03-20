#!/bin/bash

# Get the revision from command line argument
REV=$1

if [ -z "$REV" ]; then
  echo "Usage: $0 <revision-hash>"
  exit 1
fi

WORKTREE_DIR="./nixpkgs-worktree-$REV"
REPO_DIR="./nixpkgs-repo"

echo "Cleaning up worktree for revision $REV"

# Remove from Git's perspective if the repo exists
if [ -d "$REPO_DIR" ]; then
  echo "Removing worktree from Git's perspective..."
  git -C "$REPO_DIR" worktree remove --force "$WORKTREE_DIR" 2>/dev/null
  git -C "$REPO_DIR" worktree prune 2>/dev/null
fi

# Force remove the directory
if [ -d "$WORKTREE_DIR" ]; then
  echo "Forcibly removing directory $WORKTREE_DIR..."
  rm -rf "$WORKTREE_DIR"
fi

# Verify it's gone
if [ -d "$WORKTREE_DIR" ]; then
  echo "ERROR: Failed to remove directory $WORKTREE_DIR"
  exit 1
fi

echo "Worktree directory successfully removed"
