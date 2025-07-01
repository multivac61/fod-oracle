package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/nix-community/go-nix/pkg/derivation"
)

// FOD represents a fixed-output derivation
type FOD struct {
	DrvPath       string `json:"drv_path"`
	OutputPath    string `json:"output_path"`
	HashAlgorithm string `json:"hash_algorithm"`
	Hash          string `json:"hash"`
}

// ProcessingContext holds the context for processing derivations
type ProcessingContext struct {
	visited        *sync.Map
	processedPaths *sync.Map
	output         chan FOD
	wg             *sync.WaitGroup
}

func processDerivation(inputFile string, ctx *ProcessingContext) {
	defer ctx.wg.Done()

	// Check if we've already processed this derivation
	if _, alreadyProcessed := ctx.processedPaths.Load(inputFile); alreadyProcessed {
		return
	}

	// Mark this path as processed before we begin
	ctx.processedPaths.Store(inputFile, true)

	file, err := os.Open(inputFile)
	if err != nil {
		log.Printf("Error opening file %s: %v", inputFile, err)
		return
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Printf("Error reading derivation %s: %v", inputFile, err)
		return
	}

	// Check if this derivation is a FOD
	for _, out := range drv.Outputs {
		if out.HashAlgorithm != "" {
			fod := FOD{
				DrvPath:       inputFile,
				OutputPath:    out.Path,
				HashAlgorithm: out.HashAlgorithm,
				Hash:          out.Hash,
			}
			ctx.output <- fod
			break // Only need to find one FOD output per derivation
		}
	}

	// Process input derivations recursively
	for path := range drv.InputDerivations {
		if _, loaded := ctx.visited.LoadOrStore(path, true); !loaded {
			ctx.wg.Add(1)
			go processDerivation(path, ctx)
		}
	}
}

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("Usage: %s <json-file>", os.Args[0])
	}

	jsonFile := os.Args[1]

	// Read and parse JSON file
	data, err := os.ReadFile(jsonFile)
	if err != nil {
		log.Fatalf("Error reading JSON file: %v", err)
	}

	var drvPaths map[string]string
	if err := json.Unmarshal(data, &drvPaths); err != nil {
		log.Fatalf("Error parsing JSON: %v", err)
	}

	log.Printf("Processing %d derivation paths", len(drvPaths))

	// Set up processing context
	ctx := &ProcessingContext{
		visited:        &sync.Map{},
		processedPaths: &sync.Map{},
		output:         make(chan FOD, 1000),
		wg:             &sync.WaitGroup{},
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
			go processDerivation(drvPath, ctx)
		}
	}

	// Wait for all processing to complete
	ctx.wg.Wait()
	close(ctx.output)
	<-done

	log.Printf("Processing complete")
}
