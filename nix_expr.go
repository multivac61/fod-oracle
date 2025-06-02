package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nix-community/go-nix/pkg/derivation"
)

// debugLogNix logs a message only if debug mode is enabled
func debugLogNix(format string, v ...interface{}) {
	if config.Debug {
		log.Printf(format, v...)
	}
}

// isFlakeReference checks if the expression looks like a flake reference
func isFlakeReference(expr string) bool {
	// Look for common flake URI patterns
	return strings.HasPrefix(expr, "github:") ||
		strings.HasPrefix(expr, "gitlab:") ||
		strings.HasPrefix(expr, "git+") ||
		strings.HasPrefix(expr, "flake:") ||
		strings.Contains(expr, "#") && !strings.Contains(expr, "{") && !strings.Contains(expr, "(")
}

// evalFlakeReference evaluates a flake reference and returns the derivation path
func evalFlakeReference(flakeRef string) (string, error) {
	debugLogNix("Evaluating flake reference: %s", flakeRef)
	debugLogNix("DEBUG: Starting evalFlakeReference with: %s", flakeRef)

	// Check if we have a flake reference with attribute path
	parts := strings.SplitN(flakeRef, "#", 2)
	flakeURI := parts[0]
	attrPath := ""
	if len(parts) > 1 {
		attrPath = parts[1]
	}

	var cmd *exec.Cmd

	// Try using nix-instantiate first, which is more direct for getting .drv paths
	if attrPath != "" {
		// Handle different formats of attribute paths with proper escaping
		debugLogNix("Using nix-instantiate with flake reference: %s#%s", flakeURI, attrPath)

		// Use nix-instantiate which directly gives us .drv paths
		cmd = exec.Command("nix-instantiate", "--expr", fmt.Sprintf("(builtins.getFlake \"%s\").%s", flakeURI, attrPath))
		cmd.Env = append(os.Environ(), "NIXPKGS_ALLOW_UNFREE=1", "NIXPKGS_ALLOW_BROKEN=1")
	} else {
		// Just a flake URI without an attribute path - use the default package
		debugLogNix("Using nix-instantiate with default package for flake: %s", flakeURI)
		cmd = exec.Command("nix-instantiate", "--expr", fmt.Sprintf("(builtins.getFlake \"%s\").defaultPackage.x86_64-linux", flakeURI))
		cmd.Env = append(os.Environ(), "NIXPKGS_ALLOW_UNFREE=1", "NIXPKGS_ALLOW_BROKEN=1")
	}

	// Run the command
	output, err := cmd.CombinedOutput()
	debugLogNix("nix-instantiate output: %s", string(output))
	debugLogNix("nix-instantiate error: %v", err)
	if err == nil {
		// Successfully got the derivation path(s) with nix-instantiate
		outStr := strings.TrimSpace(string(output))
		lines := strings.Split(outStr, "\n")

		// Get the first valid derivation path (for multi-output cases)
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "/nix/store/") && strings.HasSuffix(line, ".drv") {
				debugLogNix("Found valid derivation path: %s", line)
				return line, nil
			}
		}
		debugLogNix("No valid derivation paths found in output")
	}

	// If nix build failed or returned invalid output, try nix eval
	debugLogNix("nix build approach failed, trying nix eval: %v", err)

	if attrPath != "" {
		// For complex attribute paths, we need to carefully construct the expression
		debugLogNix("Using nix eval with flake URI %s and attribute path %s", flakeURI, attrPath)

		// First try using direct drvPath access if the attribute is a derivation
		cmd = exec.Command("nix", "eval", "--raw", "--impure", "--expr",
			fmt.Sprintf("let pkg = %s; in builtins.tryEval (pkg.drvPath or pkg.outPath or \"\")", flakeRef))
	} else {
		// Just a flake URI without an attribute path - use the default package
		debugLogNix("Using nix eval with default package for flake: %s", flakeURI)
		cmd = exec.Command("nix", "eval", "--raw", "--impure", "--expr",
			fmt.Sprintf("let pkg = %s.defaultPackage.x86_64-linux; in pkg.drvPath or pkg.outPath or \"\"", flakeURI))
	}

	// Run the nix eval command
	output, err = cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(output))

	// Check if we got a valid derivation path
	if err == nil && strings.HasPrefix(outStr, "/nix/store/") && strings.HasSuffix(outStr, ".drv") {
		return outStr, nil
	}

	// Try another approach using nix-instantiate as a fallback
	debugLogNix("nix eval approach failed (%v), trying nix-instantiate", err)

	// First check if the flake exists
	fallbackCmd := exec.Command("nix", "flake", "show", "--json", flakeURI)
	_, fallbackErr := fallbackCmd.CombinedOutput()
	if fallbackErr != nil {
		return "", fmt.Errorf("failed to evaluate flake reference, flake does not exist: %s: %w", string(output), err)
	}

	// Flake exists, now try to get the derivation path using nix-instantiate
	debugLogNix("Flake reference resolved. Now evaluating to get derivation path...")

	// Determine how to construct the nix-instantiate expression
	var nixInstantiateExpr string
	if attrPath != "" {
		// Construct a more robust expression that handles various attribute path formats
		nixInstantiateExpr = fmt.Sprintf(`
			let 
				flake = %s;
				getAttr = attrs: path:
					let
						parts = builtins.split "\\." path;
						first = builtins.head parts;
						rest = builtins.concatStringsSep "." (builtins.tail parts);
					in
						if parts == [] then attrs
						else if builtins.length parts == 1 then attrs.${first}
						else getAttr attrs.${first} rest;
			in 
				getAttr flake "%s"
		`, flakeURI, attrPath)

		// For specific common patterns, use direct access
		if strings.HasPrefix(attrPath, "legacyPackages.") ||
			strings.HasPrefix(attrPath, "packages.") ||
			strings.HasPrefix(attrPath, "nixosConfigurations.") {
			nixInstantiateExpr = fmt.Sprintf("(%s).%s", flakeURI, attrPath)
		}
	} else {
		nixInstantiateExpr = fmt.Sprintf("(%s).defaultPackage.x86_64-linux", flakeURI)
	}

	// Create a temporary file for the expression
	tempDir, err := os.MkdirTemp("", "fod-oracle-flake")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	exprFile := filepath.Join(tempDir, "expr.nix")
	if err := os.WriteFile(exprFile, []byte(nixInstantiateExpr), 0o644); err != nil {
		return "", fmt.Errorf("failed to write expression file: %w", err)
	}

	cmd = exec.Command("nix-instantiate", exprFile)
	output, err = cmd.CombinedOutput()
	if err != nil {
		// Try a direct instantiation as last resort
		if attrPath != "" {
			cmd = exec.Command("nix-instantiate", "--expr", fmt.Sprintf("(builtins.getFlake \"%s\").%s", flakeURI, attrPath))
		} else {
			cmd = exec.Command("nix-instantiate", "--expr", fmt.Sprintf("(builtins.getFlake \"%s\").defaultPackage.x86_64-linux", flakeURI))
		}
		output, err = cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("all approaches failed to evaluate flake reference: %s: %w", string(output), err)
		}
	}

	// Get the derivation path, stripping warning messages
	outStr = strings.TrimSpace(string(output))
	lines := strings.Split(outStr, "\n")

	// Get the last line which should be the actual derivation path
	drvPath := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "/nix/store/") && strings.HasSuffix(line, ".drv") {
			drvPath = line
			break
		}
	}

	if drvPath == "" {
		return "", fmt.Errorf("no valid derivation path found in output: %s", outStr)
	}

	return drvPath, nil
}

// evalNixExpression evaluates a Nix expression and returns the derivation path
func evalNixExpression(expr string) (string, error) {
	debugLogNix("DEBUG: evalNixExpression called with: %s", expr)
	debugLogNix("DEBUG: isFlakeReference returned: %v", isFlakeReference(expr))
	// First check if this is a flake reference
	if isFlakeReference(expr) {
		debugLogNix("DEBUG: Calling evalFlakeReference")
		return evalFlakeReference(expr)
	}

	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "fod-oracle-expr")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	var cmd *exec.Cmd

	// Try to handle different types of inputs
	if !strings.Contains(expr, "let") && !strings.Contains(expr, "import") && !strings.Contains(expr, "=") {
		// Simple package name like "hello"
		debugLogNix("Evaluating as simple package: %s", expr)
		cmd = exec.Command("nix-instantiate", "<nixpkgs>", "-A", expr)
	} else {
		// Create a temporary Nix file with the expression
		exprFile := filepath.Join(tempDir, "expr.nix")
		debugLogNix("Using Nix expression file with: %s", expr)
		if err := os.WriteFile(exprFile, []byte(expr), 0o644); err != nil {
			return "", fmt.Errorf("failed to write expression file: %w", err)
		}

		cmd = exec.Command("nix-instantiate", exprFile)
	}

	// Run the command
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to evaluate expression: %s: %w", string(output), err)
	}

	// Get the derivation path, stripping warning messages
	outStr := strings.TrimSpace(string(output))
	lines := strings.Split(outStr, "\n")

	// Get the last line which should be the actual derivation path
	drvPath := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "/nix/store/") && strings.HasSuffix(line, ".drv") {
			drvPath = line
			break
		}
	}

	if drvPath == "" {
		return "", fmt.Errorf("no valid derivation path found in output: %s", outStr)
	}

	return drvPath, nil
}

// processNixExpression processes a Nix expression directly
func processNixExpression(expr string, revisionID int64, db *sql.DB, writer Writer) error {
	debugLogNix("Processing Nix expression: %s", expr)

	// Mark this as a Nix expression
	config.IsNixExpr = true

	// Evaluate the expression to get the derivation path
	drvPath, err := evalNixExpression(expr)
	if err != nil {
		return fmt.Errorf("failed to evaluate expression: %w", err)
	}

	debugLogNix("Expression evaluated to derivation: %s", drvPath)

	// Use the writer passed from main

	// Set up a context for processing
	visited := &sync.Map{}
	fodCount := 0

	// Use a channel to process derivation paths and close when we're done
	// We can't just close the channel at the end because we recursively add new
	// derivations to the channel as we go
	paths := []string{drvPath}

	// Loop through all derivations, including newly discovered ones
	for len(paths) > 0 {
		// Get the next path
		path := paths[0]
		paths = paths[1:]

		// Skip if already visited
		if _, loaded := visited.LoadOrStore(path, true); loaded {
			continue
		}

		// Process the derivation
		file, err := os.Open(path)
		if err != nil {
			debugLogNix("Error opening file %s: %v", path, err)
			continue
		}

		drv, err := derivation.ReadDerivation(file)
		file.Close()
		if err != nil {
			debugLogNix("Error reading derivation %s: %v", path, err)
			continue
		}

		// Check if this is a FOD
		for name, out := range drv.Outputs {
			if out.HashAlgorithm != "" {
				fod := FOD{
					DrvPath:       path,
					OutputPath:    out.Path,
					HashAlgorithm: out.HashAlgorithm,
					Hash:          out.Hash,
				}
				writer.AddFOD(fod)
				fodCount++
				if fodCount%10 == 0 {
					debugLogNix("Found %d FODs so far", fodCount)
				}
				if os.Getenv("VERBOSE") == "1" {
					debugLogNix("Found FOD: %s (output: %s, hash: %s)",
						filepath.Base(path), name, out.Hash)
				}
				break
			}
		}

		// Queue input derivations to process
		for inPath := range drv.InputDerivations {
			paths = append(paths, inPath)
		}
	}

	// Flush the writer
	writer.Flush()

	// Print results
	debugLogNix("Found %d FODs for expression", fodCount)

	return nil
}
