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
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/charmbracelet/fang"
	"github.com/nix-community/go-nix/pkg/derivation"
	"github.com/spf13/cobra"
)

const (
	// Buffer sizes for channels
	OutputChannelBuffer = 1000
	RebuildQueueBuffer  = 10000
	DefaultMaxParallel  = 0 // 0 means use CPU count
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
	ctx                  context.Context    // Context for cancellation
	cancelFunc           context.CancelFunc // Function to cancel context for fail-fast behavior
	visited              *sync.Map
	processedPaths       *sync.Map
	output               chan FOD
	rebuildQueue         chan FOD // Queue of FODs to rebuild
	wg                   *sync.WaitGroup
	rebuild              bool
	failOnRebuildFailure bool       // Renamed from failOnHashMismatch
	firstFailureErr      chan error // Channel for first rebuild failure (buffered size 1)
}

func processDerivation(drvPath, attrName string, ctx *ProcessingContext) {
	// Check for cancellation at the very start of the function
	select {
	case <-ctx.ctx.Done():
		ctx.wg.Done()
		return
	default:
		// Continue if not canceled
	}

	defer ctx.wg.Done()

	// Check if we've already processed this derivation
	if _, alreadyProcessed := ctx.processedPaths.Load(drvPath); alreadyProcessed {
		return
	}

	// Mark this path as processed before we begin
	ctx.processedPaths.Store(drvPath, true)

	file, err := os.Open(drvPath)
	if err != nil {
		log.Printf("Error opening derivation file %s: %v", drvPath, err)
		// Send error FOD to output for tracking, but check for cancellation
		errorFOD := FOD{
			DrvPath:       drvPath,
			Attr:          attrName,
			RebuildStatus: "error",
			ErrorMessage:  fmt.Sprintf("Failed to open derivation: %v", err),
		}
		select {
		case ctx.output <- errorFOD:
		case <-ctx.ctx.Done():
			return
		}
		return
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Printf("Error reading derivation %s: %v", drvPath, err)
		// Send error FOD to output for tracking, but check for cancellation
		errorFOD := FOD{
			DrvPath:       drvPath,
			Attr:          attrName,
			RebuildStatus: "error",
			ErrorMessage:  fmt.Sprintf("Failed to parse derivation: %v", err),
		}
		select {
		case ctx.output <- errorFOD:
		case <-ctx.ctx.Done():
			return
		}
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

			// Send FOD to output channel first, but check for cancellation
			select {
			case ctx.output <- fod:
			case <-ctx.ctx.Done():
				return
			}

			// If rebuild flag is set, queue the FOD for rebuilding
			if ctx.rebuild {
				select {
				case ctx.rebuildQueue <- fod: // Blocking send ensures we never miss FODs
				case <-ctx.ctx.Done():
					return
				}
			}
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

// rebuildFOD attempts to rebuild a FOD from source to verify it works.
// It explicitly disables binary caches to ensure a local, from-source build.
// This function is now pure: it has no side effects on ProcessingContext.
func rebuildFOD(procCtx context.Context, fod *FOD) error {
	log.Printf("Verifying FOD from source (no cache): %s", fod.DrvPath)

	// Check if the output already exists in the store
	if _, err := os.Stat(fod.OutputPath); os.IsNotExist(err) {
		// Output doesn't exist, build without --check first
		log.Printf("Building FOD from source: %s", fod.DrvPath)
		buildCmd := exec.CommandContext(procCtx, "nix-build", fod.DrvPath, "--no-out-link", "--no-substitute")
		if output, err := buildCmd.CombinedOutput(); err != nil {
			return handleBuildFailure(fod, err, output)
		}
	}

	// Now verify with --check (output should exist by now)
	log.Printf("Verifying FOD with --check: %s", fod.DrvPath)
	checkCmd := exec.CommandContext(procCtx, "nix-build", fod.DrvPath, "--no-out-link", "--no-substitute", "--check")
	if output, err := checkCmd.CombinedOutput(); err != nil {
		return handleBuildFailure(fod, err, output)
	}

	// Success case
	fod.RebuildStatus = "success"
	fod.HashMismatch = false
	log.Printf("FOD verification successful: %s", fod.DrvPath)
	return nil
}

// handleBuildFailure processes build failures and annotates the FOD accordingly
func handleBuildFailure(fod *FOD, err error, output []byte) error {
	errStr := string(output)

	if strings.Contains(errStr, "hash mismatch") {
		fod.RebuildStatus = "verification_failure"
		fod.ErrorMessage = fmt.Sprintf("Verification failed: %v\nOutput: %s", err, errStr)
		fod.HashMismatch = true

		re := regexp.MustCompile(`got:\s*([a-zA-Z0-9-/+=]+)`) // Expanded regex for base64 SRI hashes
		if matches := re.FindStringSubmatch(errStr); len(matches) > 1 {
			fod.ActualHash = matches[1]
		}
	} else {
		fod.RebuildStatus = "dependency_failure"
		fod.ErrorMessage = fmt.Sprintf("Failed to build from source: %v\nOutput: %s", err, errStr)
	}

	// Return a detailed error that includes the build output for debugging
	return fmt.Errorf("rebuild of %s failed: %s\n\nBuild output:\n%s", fod.DrvPath, fod.RebuildStatus, errStr)
}

// normalizeToSRI converts a hash to SRI format if it isn't already
func normalizeToSRI(hash, hashAlgorithm string) string {
	// If it's already SRI format, return as-is
	if strings.Contains(hash, "-") {
		return hash
	}

	// Extract the algorithm
	algo := strings.TrimPrefix(hashAlgorithm, "r:")

	// Convert nix hash to SRI format using nix command
	cmd := exec.Command("nix", "hash", "to-sri", "--type", algo, hash)
	output, err := cmd.Output()
	if err != nil {
		// If conversion fails, return original hash
		return hash
	}
	return strings.TrimSpace(string(output))
}

// initializeProcessing creates and returns a new ProcessingContext
func initializeProcessing(ctx context.Context, cancel context.CancelFunc, rebuild, strictRebuild bool) *ProcessingContext {
	return &ProcessingContext{
		ctx:                  ctx,
		cancelFunc:           cancel,
		visited:              &sync.Map{},
		processedPaths:       &sync.Map{},
		output:               make(chan FOD, OutputChannelBuffer),
		rebuildQueue:         make(chan FOD, RebuildQueueBuffer),
		wg:                   &sync.WaitGroup{},
		rebuild:              rebuild,
		failOnRebuildFailure: strictRebuild,
		firstFailureErr:      make(chan error, 1), // Buffered channel for the first failure
	}
}

// startOutputWriter starts the goroutine that writes FODs to stdout
func startOutputWriter(ctx *ProcessingContext) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.ctx.Done(): // Check for cancellation
				return // Exit the output writer goroutine
			case fod, ok := <-ctx.output:
				if !ok {
					return // Channel is closed and empty
				}
				output, _ := json.Marshal(fod)
				fmt.Println(string(output))
			}
		}
	}()
	return done
}

// startRebuildWorkers starts the worker pool for rebuilding FODs
func startRebuildWorkers(ctx *ProcessingContext, maxParallel int) chan struct{} {
	rebuildDone := make(chan struct{})
	if !ctx.rebuild {
		close(rebuildDone)
		return rebuildDone
	}

	go func() {
		defer close(rebuildDone)

		// Create worker pool
		workerWg := &sync.WaitGroup{}
		for range maxParallel {
			workerWg.Add(1)
			go func() {
				defer workerWg.Done()
				for {
					select {
					case <-ctx.ctx.Done(): // Check for cancellation
						return // Exit the worker goroutine
					case fod, ok := <-ctx.rebuildQueue:
						if !ok {
							return // Channel is closed and empty
						}

						// Call the pure rebuildFOD function
						if err := rebuildFOD(ctx.ctx, &fod); err != nil {
							// A failure occurred!
							if ctx.failOnRebuildFailure {
								// Try to send the error. This will only succeed for the first one.
								select {
								case ctx.firstFailureErr <- err:
									ctx.cancelFunc() // Cancel everyone else *after* reporting our error.
								default:
									// Channel is already full, another goroutine reported the error first.
								}
							}
						}

						// Always send the (updated) FOD to the output channel.
						select {
						case ctx.output <- fod:
						case <-ctx.ctx.Done():
							return
						}

						// Check for cancellation before starting next loop
						if ctx.ctx.Err() != nil {
							return
						}
					}
				}
			}()
		}

		workerWg.Wait()
	}()

	return rebuildDone
}

func evaluateNixExpression(ctx context.Context, expr string, isFlake bool) (map[string]string, error) {
	// Use nix-eval-jobs under the hood
	var cmd *exec.Cmd
	if isFlake {
		cmd = exec.CommandContext(ctx, "nix-eval-jobs", "--flake", expr)
	} else {
		cmd = exec.CommandContext(ctx, "nix-eval-jobs", "--expr", expr)
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
	// Set up a PARENT context that handles OS signals (Ctrl+C)
	signalCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	// Create a CHILD context that we can cancel ourselves for fail-fast behavior
	// This inherits cancellation from the parent
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel() // Ensure cancel is called on exit to clean up resources
	debug, _ := cmd.Flags().GetBool("debug")
	isFlake, _ := cmd.Flags().GetBool("flake")
	isExpr, _ := cmd.Flags().GetBool("expr")
	rebuild, _ := cmd.Flags().GetBool("rebuild")
	strictRebuild, _ := cmd.Flags().GetBool("strict-rebuild")
	maxParallel, _ := cmd.Flags().GetInt("max-parallel")

	// Set default max-parallel to CPU count if not specified
	if maxParallel == DefaultMaxParallel {
		maxParallel = runtime.NumCPU()
	}

	// Validate flag combinations
	if strictRebuild && !rebuild {
		return fmt.Errorf("--strict-rebuild can only be used with --rebuild")
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
	drvPaths, err := evaluateNixExpression(ctx, expr, isFlake)
	if err != nil {
		return fmt.Errorf("error evaluating expression: %v", err)
	}

	log.Printf("Processing %d derivation paths", len(drvPaths))

	// Initialize processing context
	pCtx := initializeProcessing(ctx, cancel, rebuild, strictRebuild)

	// Start workers
	done := startOutputWriter(pCtx)
	rebuildDone := startRebuildWorkers(pCtx, maxParallel)

	// Process all derivation paths in parallel
	for name, drvPath := range drvPaths {
		if _, err := os.Stat(drvPath); err != nil {
			log.Printf("Warning: Derivation file does not exist: %s (%s)", drvPath, name)
			continue
		}

		if _, loaded := pCtx.visited.LoadOrStore(drvPath, true); !loaded {
			pCtx.wg.Add(1)
			go processDerivation(drvPath, name, pCtx)
		}
	}

	// Wait for all processing to complete
	pCtx.wg.Wait()

	// Close rebuild queue and wait for workers to finish or fast failure
	if rebuild && strictRebuild {
		close(pCtx.rebuildQueue)

		// Use select to handle either successful completion or fast failure
		select {
		case failureErr := <-pCtx.firstFailureErr:
			// A fast failure was triggered. Wait for workers to finish, then close output.
			<-rebuildDone
			close(pCtx.output)
			<-done
			return failureErr
		case <-rebuildDone:
			// All rebuilds completed without a strict failure.
			log.Printf("All rebuilds completed successfully.")
		}
	} else if rebuild {
		close(pCtx.rebuildQueue)
		<-rebuildDone
	}

	close(pCtx.output)
	<-done

	log.Printf("Processing complete")
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

  # Rebuild and fail if any rebuild fails for any reason
  fod-oracle --rebuild --strict-rebuild 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'

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
	rootCmd.Flags().Bool("strict-rebuild", false, "Exit with error code if any rebuild fails for any reason (requires --rebuild)")
	rootCmd.Flags().Int("max-parallel", DefaultMaxParallel, "Maximum number of parallel rebuilds (default: CPU count)")

	// Execute with fang for fancy output
	if err := fang.Execute(context.Background(), rootCmd); err != nil {
		os.Exit(1)
	}
}
