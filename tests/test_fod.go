package tests

import (
	"fmt"
	"log"
	"os"

	"github.com/nix-community/go-nix/pkg/derivation"
)

func RunTestFOD() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: test_fod <derivation_path>")
		os.Exit(1)
	}

	drvPath := os.Args[1]
	log.Printf("Testing derivation: %s", drvPath)

	// Check if file exists
	if _, err := os.Stat(drvPath); os.IsNotExist(err) {
		log.Fatalf("Derivation file does not exist: %s", drvPath)
	}

	// Open and read the derivation
	file, err := os.Open(drvPath)
	if err != nil {
		log.Fatalf("Error opening file: %v", err)
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Fatalf("Error reading derivation: %v", err)
	}

	// Check outputs
	log.Printf("Derivation has %d outputs", len(drv.Outputs))

	isFOD := false
	for name, out := range drv.Outputs {
		log.Printf("Output %s: Path=%s, HashAlgo=%s, Hash=%s",
			name, out.Path, out.HashAlgorithm, out.Hash)

		if out.HashAlgorithm != "" {
			isFOD = true
			log.Printf("This is a FOD with hash algorithm: %s and hash: %s",
				out.HashAlgorithm, out.Hash)
		}
	}

	// Check environment variables
	log.Printf("Environment variables:")
	for key, value := range drv.Env {
		if key == "outputHash" || key == "outputHashAlgo" || key == "outputHashMode" {
			log.Printf("  %s = %s", key, value)
		}
	}

	if isFOD {
		log.Printf("✅ Derivation is a FOD")
	} else {
		log.Printf("❌ Derivation is NOT a FOD")
	}
}
