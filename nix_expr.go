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

// evalNixExpression evaluates a Nix expression and returns the derivation path
func evalNixExpression(expr string) (string, error) {
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
		log.Printf("Evaluating as simple package: %s", expr)
		cmd = exec.Command("nix-instantiate", "<nixpkgs>", "-A", expr)
	} else {
		// Create a temporary Nix file with the expression
		exprFile := filepath.Join(tempDir, "expr.nix")
		log.Printf("Using Nix expression file with: %s", expr)
		if err := os.WriteFile(exprFile, []byte(expr), 0644); err != nil {
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
func processNixExpression(expr string, revisionID int64, db *sql.DB) error {
	log.Printf("Processing Nix expression: %s", expr)
	
	// Mark this as a Nix expression
	config.IsNixExpr = true
	
	// Evaluate the expression to get the derivation path
	drvPath, err := evalNixExpression(expr)
	if err != nil {
		return fmt.Errorf("failed to evaluate expression: %w", err)
	}
	
	log.Printf("Expression evaluated to derivation: %s", drvPath)
	
	// Initialize the writer
	writer, err := GetWriter(db, revisionID, "expr")
	if err != nil {
		return fmt.Errorf("failed to create writer: %w", err)
	}
	defer writer.Close()
	
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
			log.Printf("Error opening file %s: %v", path, err)
			continue
		}
		
		drv, err := derivation.ReadDerivation(file)
		file.Close()
		if err != nil {
			log.Printf("Error reading derivation %s: %v", path, err)
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
					log.Printf("Found %d FODs so far", fodCount)
				}
				if os.Getenv("VERBOSE") == "1" {
					log.Printf("Found FOD: %s (output: %s, hash: %s)",
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
	if config.OutputFormat == "sqlite" {
		var dbFodCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?", revisionID).Scan(&dbFodCount); err != nil {
			log.Printf("Error counting FODs: %v", err)
		}
		log.Printf("Found %d FODs for expression (stored %d in database)", fodCount, dbFodCount)
	} else {
		log.Printf("Found %d FODs for expression", fodCount)
	}
	
	return nil
}