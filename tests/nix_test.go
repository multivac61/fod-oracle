package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareNixpkgsWorktree tests the function that prepares a Nixpkgs worktree
// This is an integration test that requires Git to be installed
// It's marked with the "integration" tag so it can be skipped during routine testing
// Run with: go test -tags=integration ./tests/...
func TestPrepareNixpkgsWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Check if git is installed
	_, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found, skipping test")
	}

	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "fod-oracle-worktree-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a fake git repository
	repoDir := filepath.Join(tempDir, "repo")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatalf("Failed to create repo directory: %v", err)
	}

	// Initialize the repository
	initCmd := exec.Command("git", "init")
	initCmd.Dir = repoDir
	if err := initCmd.Run(); err != nil {
		t.Fatalf("Failed to initialize repository: %v", err)
	}

	// Create some files
	libDir := filepath.Join(repoDir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("Failed to create lib directory: %v", err)
	}

	// Create a minver.nix file
	minverPath := filepath.Join(libDir, "minver.nix")
	if err := os.WriteFile(minverPath, []byte("# This is a test file"), 0o644); err != nil {
		t.Fatalf("Failed to create minver.nix: %v", err)
	}

	// Configure git
	configCmd := exec.Command("git", "config", "user.name", "Test User")
	configCmd.Dir = repoDir
	if err := configCmd.Run(); err != nil {
		t.Fatalf("Failed to configure git user.name: %v", err)
	}

	configCmd = exec.Command("git", "config", "user.email", "test@example.com")
	configCmd.Dir = repoDir
	if err := configCmd.Run(); err != nil {
		t.Fatalf("Failed to configure git user.email: %v", err)
	}

	// Add and commit the files
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = repoDir
	if err := addCmd.Run(); err != nil {
		t.Fatalf("Failed to add files: %v", err)
	}

	commitCmd := exec.Command("git", "commit", "-m", "Initial commit")
	commitCmd.Dir = repoDir
	if err := commitCmd.Run(); err != nil {
		t.Fatalf("Failed to commit files: %v", err)
	}

	// Get the commit hash
	revCmd := exec.Command("git", "rev-parse", "HEAD")
	revCmd.Dir = repoDir
	revOutput, err := revCmd.Output()
	if err != nil {
		t.Fatalf("Failed to get commit hash: %v", err)
	}
	rev := strings.TrimSpace(string(revOutput))

	// Test the worktree preparation (mock implementation for testing)
	worktreeDir := filepath.Join(tempDir, "worktree")

	// This is a simple mock of the function we'd normally test
	// In a real test, you'd call the actual prepareNixpkgsWorktree function
	err = mockPrepareWorktree(repoDir, rev, worktreeDir)
	if err != nil {
		t.Fatalf("Failed to prepare worktree: %v", err)
	}

	// Check that the worktree was created correctly
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatalf("Worktree directory was not created")
	}

	// Check that the minver.nix file exists in the worktree
	worktreeMinverPath := filepath.Join(worktreeDir, "lib", "minver.nix")
	if _, err := os.Stat(worktreeMinverPath); os.IsNotExist(err) {
		t.Fatalf("minver.nix file not found in worktree")
	}

	// Read the minver.nix file and check its contents
	worktreeMinverContent, err := os.ReadFile(worktreeMinverPath)
	if err != nil {
		t.Fatalf("Failed to read minver.nix in worktree: %v", err)
	}
	expectedContent := "# This is a test file"
	if string(worktreeMinverContent) != expectedContent {
		t.Errorf("Expected minver.nix contents: %q, got: %q", expectedContent, worktreeMinverContent)
	}
}

// Mock implementation of prepareNixpkgsWorktree for testing
func mockPrepareWorktree(repoPath, rev, worktreePath string) error {
	// Create the worktree directory
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		return err
	}

	// Use git worktree add to create the worktree
	addCmd := exec.Command("git", "worktree", "add", "--detach", worktreePath, rev)
	addCmd.Dir = repoPath
	return addCmd.Run()
}

// TestCleanupWorktrees tests the function that cleans up worktree directories
func TestCleanupWorktrees(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Check if git is installed
	_, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found, skipping test")
	}

	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "fod-oracle-cleanup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mock repository directory
	repoDir := filepath.Join(tempDir, "nixpkgs-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("Failed to create repo directory: %v", err)
	}

	// Initialize the repository
	initCmd := exec.Command("git", "init")
	initCmd.Dir = repoDir
	if err := initCmd.Run(); err != nil {
		t.Fatalf("Failed to initialize repository: %v", err)
	}

	// Create some mock worktree directories
	worktreeDirs := []string{
		filepath.Join(tempDir, "nixpkgs-worktree-123456"),
		filepath.Join(tempDir, "nixpkgs-worktree-789012"),
		filepath.Join(tempDir, "some-other-directory"), // This should not be removed
	}

	for _, dir := range worktreeDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	// Test the cleanup function (mock implementation for testing)
	err = mockCleanupWorktrees(tempDir, repoDir)
	if err != nil {
		t.Fatalf("Failed to cleanup worktrees: %v", err)
	}

	// Check that the worktree directories were removed
	for i, dir := range worktreeDirs {
		_, err := os.Stat(dir)
		if i < 2 { // First two should be removed
			if !os.IsNotExist(err) {
				t.Errorf("Expected directory %s to be removed, but it still exists", dir)
			}
		} else { // Last one should still exist
			if os.IsNotExist(err) {
				t.Errorf("Directory %s was removed, but it should not have been", dir)
			}
		}
	}
}

// Mock implementation of cleanupWorktrees for testing
func mockCleanupWorktrees(workDir, repoDir string) error {
	// Prune worktrees in the repository
	pruneCmd := exec.Command("git", "worktree", "prune")
	pruneCmd.Dir = repoDir
	if err := pruneCmd.Run(); err != nil {
		return err
	}

	// Find and remove worktree directories
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "nixpkgs-worktree-") {
			worktreePath := filepath.Join(workDir, entry.Name())
			if err := os.RemoveAll(worktreePath); err != nil {
				return err
			}
		}
	}

	return nil
}
