package main

import (
	"fmt"
	"os"

	"github.com/nix-community/go-nix/pkg/derivation"
)

func main() {
	inputFile := os.Args[1]
	drvs := make(map[string]struct{}, 0)
	traverse(inputFile, drvs)
}

func traverse(inputFile string, visited map[string]struct{}) {
	_, ok := visited[inputFile]
	if ok {
		return
	}
	visited[inputFile] = struct{}{}

	file, err := os.Open(inputFile)
	if err != nil {
		fmt.Println(err)
		return
	}

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Find output hash
	for _, out := range drv.Outputs {
		if out.HashAlgorithm != "" {
			// Know we know it's a FOD
			fmt.Println(out)
		}
	}

	for path := range drv.InputDerivations {
		traverse(path, visited)
	}
}
