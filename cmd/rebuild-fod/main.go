package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/multivac61/fod-oracle/pkg/fod"
)

const programName = "rebuild-fod"

// This file enables standalone building of the rebuild-fod tool.
// Use this for building the tool independently: go build -o rebuild-fod

// The actual implementation is in rebuild_fod.go in the main package,
// which can be imported and used directly from other Go code.

// Default timeout in seconds
const defaultTimeoutSeconds = 300 // 5 minutes

// This file exists solely to provide a main() entry point for standalone builds.
func printHelp() {
	fmt.Fprintf(os.Stderr, "rebuild-fod - Rebuild and verify Fixed-Output Derivations\n\n")
	fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] DRV_PATH\n\n", programName)
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help            Show this help message\n")
	fmt.Fprintf(os.Stderr, "  -t, --timeout SECONDS Set timeout in seconds (default: %d)\n\n", defaultTimeoutSeconds)
	fmt.Fprintf(os.Stderr, "Arguments:\n")
	fmt.Fprintf(os.Stderr, "  DRV_PATH      Path to a fixed-output derivation to rebuild and verify\n\n")
	fmt.Fprintf(os.Stderr, "Example:\n")
	fmt.Fprintf(os.Stderr, "  %s /nix/store/y0jggj3ycygz74vn1a6bdynx7k0478fb-AMB-plugins-0.8.1.tar.bz2.drv\n", programName)
	fmt.Fprintf(os.Stderr, "  %s --timeout 60 /nix/store/y0jggj3ycygz74vn1a6bdynx7k0478fb-AMB-plugins-0.8.1.tar.bz2.drv\n\n", programName)
	fmt.Fprintf(os.Stderr, "Description:\n")
	fmt.Fprintf(os.Stderr, "  The rebuild-fod tool rebuilds a fixed-output derivation and attempts to verify\n")
	fmt.Fprintf(os.Stderr, "  its content hash. It uses multiple methods to determine the hash:\n")
	fmt.Fprintf(os.Stderr, "  1. Extracting from derivation JSON (Method 1)\n")
	fmt.Fprintf(os.Stderr, "  2. Querying the Nix store (Method 2)\n")
	fmt.Fprintf(os.Stderr, "  3. Computing from the output (Method 3)\n")
	fmt.Fprintf(os.Stderr, "  4. Building the derivation if needed (Method 4)\n\n")
	fmt.Fprintf(os.Stderr, "  The tool will report if there's a hash mismatch, which could indicate\n")
	fmt.Fprintf(os.Stderr, "  reproducibility issues.\n")
}

// Parse timeout value from string to int
func parseTimeout(s string) (int, error) {
	timeout, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout value: %s", s)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("timeout must be greater than 0")
	}
	return timeout, nil
}

func main() {
	// Default timeout
	timeoutSeconds := defaultTimeoutSeconds
	var drvPath string
	
	// Parse arguments
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		
		// Check for help flag
		if arg == "--help" || arg == "-h" {
			printHelp()
			return
		}
		
		// Handle timeout flag
		if arg == "--timeout" || arg == "-t" {
			if i+1 < len(args) {
				i++
				var err error
				timeoutSeconds, err = parseTimeout(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
					fmt.Fprintf(os.Stderr, "Use --help for more information\n")
					os.Exit(1)
				}
			} else {
				fmt.Fprintf(os.Stderr, "Error: Missing value for timeout flag\n\n")
				fmt.Fprintf(os.Stderr, "Use --help for more information\n")
				os.Exit(1)
			}
		} else if !strings.HasPrefix(arg, "-") {
			// If not a flag, treat as derivation path
			drvPath = arg
		} else {
			fmt.Fprintf(os.Stderr, "Error: Unknown option: %s\n\n", arg)
			fmt.Fprintf(os.Stderr, "Use --help for more information\n")
			os.Exit(1)
		}
	}
	
	// Check if we got a derivation path
	if drvPath == "" {
		fmt.Fprintf(os.Stderr, "Error: Missing derivation path\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] DRV_PATH\n", programName)
		fmt.Fprintf(os.Stderr, "Use --help for more information\n")
		os.Exit(1)
	}
	
	// Set up a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	
	// Run the rebuild
	result, err := fod.RebuildFOD(ctx, drvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	
	// Print the full log
	fmt.Println(result.Log)
	
	// Exit with appropriate status code
	if result.Status != "success" {
		os.Exit(1)
	}
}