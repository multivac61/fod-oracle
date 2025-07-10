package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/charmbracelet/fang"
	"github.com/nix-community/go-nix/pkg/derivation"
	"github.com/spf13/cobra"
)

// FOD represents a fixed-output derivation
type FOD struct {
	DrvPath       string `json:"drv_path"`
	OutputPath    string `json:"output_path"`
	HashAlgorithm string `json:"hash_algorithm"`
	Hash          string `json:"hash"`
	Attr          string `json:"attr,omitempty"`
	// Rebuild fields
	RebuildStatus string `json:"rebuild_status,omitempty"`
	ActualHash    string `json:"actual_hash,omitempty"`
	HashMismatch  bool   `json:"hash_mismatch,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

// ProcessingContext holds the context for processing derivations
type ProcessingContext struct {
	visited            *sync.Map
	processedPaths     *sync.Map
	output             chan FOD
	wg                 *sync.WaitGroup
	rebuild            bool
	failOnHashMismatch bool
	hashMismatchFound  *sync.Map // Track if any hash mismatches were found
}

func processDerivation(drvPath, attrName string, ctx *ProcessingContext) {
	defer ctx.wg.Done()

	// Check if we've already processed this derivation
	if _, alreadyProcessed := ctx.processedPaths.Load(drvPath); alreadyProcessed {
		return
	}

	// Mark this path as processed before we begin
	ctx.processedPaths.Store(drvPath, true)

	file, err := os.Open(drvPath)
	if err != nil {
		log.Printf("Error opening file %s: %v", drvPath, err)
		return
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Printf("Error reading derivation %s: %v", drvPath, err)
		return
	}

	// Check if this derivation is a FOD
	for _, out := range drv.Outputs {
		if out.HashAlgorithm != "" {
			// Normalize hash to SRI format
			sriHash := normalizeToSRI(out.Hash, out.HashAlgorithm)

			fod := FOD{
				DrvPath:       drvPath,
				OutputPath:    out.Path,
				HashAlgorithm: out.HashAlgorithm,
				Hash:          sriHash,
				Attr:          attrName,
			}

			// If rebuild flag is set, attempt to rebuild the FOD
			if ctx.rebuild {
				rebuildFOD(&fod, ctx)
			}

			ctx.output <- fod
			break // Only need to find one FOD output per derivation
		}
	}

	// Process input derivations recursively
	for inputDrvPath := range drv.InputDerivations {
		if _, loaded := ctx.visited.LoadOrStore(inputDrvPath, true); !loaded {
			ctx.wg.Add(1)
			go processDerivation(inputDrvPath, "", ctx)
		}
	}
}

// rebuildFOD attempts to rebuild a FOD and compare hashes
func rebuildFOD(fod *FOD, ctx *ProcessingContext) {
	log.Printf("Rebuilding FOD: %s", fod.DrvPath)

	// Use nix-build to rebuild the derivation
	cmd := exec.Command("nix-build", fod.DrvPath, "--no-out-link")
	output, err := cmd.CombinedOutput()

	if err != nil {
		fod.RebuildStatus = "failure"
		fod.ErrorMessage = fmt.Sprintf("Build failed: %v\nOutput: %s", err, string(output))
		return
	}

	// Parse the output to get the built store path
	outputLines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(outputLines) == 0 {
		fod.RebuildStatus = "failure"
		fod.ErrorMessage = "No output path from nix-build"
		return
	}

	builtPath := strings.TrimSpace(outputLines[len(outputLines)-1])

	// Compute the actual SRI hash of the built output
	actualHash, err := computeSRIHash(builtPath, fod.HashAlgorithm)
	if err != nil {
		fod.RebuildStatus = "failure"
		fod.ErrorMessage = fmt.Sprintf("Failed to compute SRI hash of built output: %v", err)
		return
	}

	fod.ActualHash = actualHash
	fod.RebuildStatus = "success"

	// Compare SRI hashes
	expectedHash := normalizeToSRI(fod.Hash, fod.HashAlgorithm)

	if expectedHash != actualHash {
		fod.HashMismatch = true
		// Track that we found a hash mismatch
		ctx.hashMismatchFound.Store("found", true)
	}
}

// computeSRIHash computes the SRI hash of a file or directory
func computeSRIHash(path, hashAlgorithm string) (string, error) {
	// Extract the algorithm from the hash algorithm string
	algo := hashAlgorithm
	if strings.HasPrefix(algo, "r:") {
		// Recursive hash for directories
		algo = strings.TrimPrefix(algo, "r:")
		cmd := exec.Command("nix-hash", "--type", algo, "--base32", path)
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to compute recursive hash: %v", err)
		}
		nixHash := strings.TrimSpace(string(output))

		// Convert nix hash to SRI format
		cmd = exec.Command("nix", "hash", "to-sri", "--type", algo, nixHash)
		sriOutput, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to convert to SRI: %v", err)
		}
		return strings.TrimSpace(string(sriOutput)), nil
	} else {
		// Regular file hash
		cmd := exec.Command("nix-hash", "--type", algo, "--base32", path)
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to compute file hash: %v", err)
		}
		nixHash := strings.TrimSpace(string(output))

		// Convert nix hash to SRI format
		cmd = exec.Command("nix", "hash", "to-sri", "--type", algo, nixHash)
		sriOutput, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to convert to SRI: %v", err)
		}
		return strings.TrimSpace(string(sriOutput)), nil
	}
}

// normalizeToSRI converts a hash to SRI format if it isn't already
func normalizeToSRI(hash, hashAlgorithm string) string {
	// If it's already SRI format, return as-is
	if strings.Contains(hash, "-") {
		return hash
	}

	// Extract the algorithm
	algo := hashAlgorithm
	if strings.HasPrefix(algo, "r:") {
		algo = strings.TrimPrefix(algo, "r:")
	}

	// Convert nix hash to SRI format using nix command
	cmd := exec.Command("nix", "hash", "to-sri", "--type", algo, hash)
	output, err := cmd.Output()
	if err != nil {
		// If conversion fails, return original hash
		return hash
	}
	return strings.TrimSpace(string(output))
}

func evaluateNixExpression(expr string, isFlake bool) (map[string]string, error) {
	// Use nix-eval-jobs under the hood
	var cmd *exec.Cmd
	if isFlake {
		cmd = exec.Command("nix-eval-jobs", "--flake", expr)
	} else {
		cmd = exec.Command("nix-eval-jobs", "--expr", expr)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start nix-eval-jobs: %v", err)
	}

	drvPaths := make(map[string]string)
	scanner := bufio.NewScanner(stdout)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var result struct {
			Attr    string `json:"attr"`
			DrvPath string `json:"drvPath"`
		}

		if err := json.Unmarshal([]byte(line), &result); err != nil {
			log.Printf("Warning: failed to parse JSON line: %v", err)
			continue
		}

		if result.DrvPath != "" {
			drvPaths[result.Attr] = result.DrvPath
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading nix-eval-jobs output: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("nix-eval-jobs failed: %v", err)
	}

	return drvPaths, nil
}

func runFODOracle(cmd *cobra.Command, args []string) error {
	debug, _ := cmd.Flags().GetBool("debug")
	isFlake, _ := cmd.Flags().GetBool("flake")
	isExpr, _ := cmd.Flags().GetBool("expr")
	rebuild, _ := cmd.Flags().GetBool("rebuild")
	failOnHashMismatch, _ := cmd.Flags().GetBool("fail-on-hash-mismatch")

	// Validate flag combinations
	if failOnHashMismatch && !rebuild {
		return fmt.Errorf("--fail-on-hash-mismatch can only be used with --rebuild")
	}

	// Set log output based on debug flag
	if debug {
		log.SetOutput(os.Stderr)
	} else {
		log.SetOutput(io.Discard) // Discard all log output
	}

	if len(args) != 1 {
		return fmt.Errorf("exactly one expression argument is required")
	}

	expr := args[0]

	// Auto-detect flake if not specified
	if !isExpr && !isFlake {
		if strings.Contains(expr, "#") || strings.HasPrefix(expr, "github:") || strings.HasPrefix(expr, "gitlab:") {
			isFlake = true
		} else {
			isExpr = true
		}
	}

	log.Printf("Evaluating expression: %s (flake: %v, rebuild: %v)", expr, isFlake, rebuild)

	// Evaluate the Nix expression to get derivation paths
	drvPaths, err := evaluateNixExpression(expr, isFlake)
	if err != nil {
		return fmt.Errorf("error evaluating expression: %v", err)
	}

	log.Printf("Processing %d derivation paths", len(drvPaths))

	// Set up processing context
	ctx := &ProcessingContext{
		visited:            &sync.Map{},
		processedPaths:     &sync.Map{},
		output:             make(chan FOD, 1000),
		wg:                 &sync.WaitGroup{},
		rebuild:            rebuild,
		failOnHashMismatch: failOnHashMismatch,
		hashMismatchFound:  &sync.Map{},
	}

	// Start output writer goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		for fod := range ctx.output {
			output, _ := json.Marshal(fod)
			fmt.Println(string(output))
		}
	}()

	// Process all derivation paths in parallel
	for name, drvPath := range drvPaths {
		if _, err := os.Stat(drvPath); err != nil {
			log.Printf("Warning: Derivation file does not exist: %s (%s)", drvPath, name)
			continue
		}

		if _, loaded := ctx.visited.LoadOrStore(drvPath, true); !loaded {
			ctx.wg.Add(1)
			go processDerivation(drvPath, name, ctx)
		}
	}

	// Wait for all processing to complete
	ctx.wg.Wait()
	close(ctx.output)
	<-done

	log.Printf("Processing complete")
	
	// Check if we should fail on hash mismatches
	if failOnHashMismatch {
		if _, found := ctx.hashMismatchFound.Load("found"); found {
			return fmt.Errorf("hash mismatch detected - exiting with error as requested")
		}
	}
	
	return nil
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "fod-oracle [flags] <expression>",
		Short: "Find Fixed Output Derivations in Nix expressions",
		Long: `fod-oracle finds all Fixed Output Derivations (FODs) in a Nix expression.

FODs are derivations with predetermined output hashes, typically used for source code,
patches, and other fixed content that needs to be downloaded from external sources.`,
		Example: `  # Find FODs in a flake package
  fod-oracle 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'

  # Find FODs and rebuild them to verify hashes
  fod-oracle --rebuild 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'

  # Rebuild and fail if any hash mismatches are found
  fod-oracle --rebuild --fail-on-hash-mismatch 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'

  # Use a regular Nix expression
  fod-oracle --expr 'import <nixpkgs> {}.hello'

  # Enable debug output
  fod-oracle --debug 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'`,
		Args: cobra.ExactArgs(1),
		RunE: runFODOracle,
	}

	rootCmd.Flags().Bool("debug", false, "Enable debug output to stderr")
	rootCmd.Flags().Bool("flake", false, "Evaluate a flake expression")
	rootCmd.Flags().Bool("expr", false, "Treat the argument as a Nix expression")
	rootCmd.Flags().Bool("rebuild", false, "Rebuild FODs to verify their hashes")
	rootCmd.Flags().Bool("fail-on-hash-mismatch", false, "Exit with error code if hash mismatches are found (requires --rebuild)")

	// Execute with fang for fancy output
	if err := fang.Execute(context.Background(), rootCmd); err != nil {
		os.Exit(1)
	}
}
