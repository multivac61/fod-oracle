package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/nix-community/go-nix/pkg/derivation"
)

func callNixEvalJobs(visitChan chan<- string, errorChan chan<- error) {
	cmd := exec.Command("nix-eval-jobs", "bar.nix") // Customize arguments as needed
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		errorChan <- fmt.Errorf("failed to create stdout pipe: %w", err)
		return
	}

	err = cmd.Start()
	if err != nil {
		errorChan <- fmt.Errorf("failed to start nix-eval-jobs: %w", err)
		return
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		var result struct {
			DrvPath string `json:"drvPath"`
		}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			errorChan <- fmt.Errorf("failed to parse JSON: %w", err)
			continue
		}
		if result.DrvPath != "" {
			visitChan <- result.DrvPath
		}
	}

	if err := scanner.Err(); err != nil {
		errorChan <- fmt.Errorf("error reading stdout: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		errorChan <- fmt.Errorf("nix-eval-jobs command failed: %w", err)
	}

	close(visitChan)
}

func findAllFODs() map[string]struct{} {
	visitChan := make(chan string, 100)
	errorChan := make(chan error, 10)
	drvs := make(map[string]struct{})

	// Start nix-eval-jobs in a goroutine
	go callNixEvalJobs(visitChan, errorChan)

	// Process errors in a separate goroutine
	go func() {
		for err := range errorChan {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}()

	// Process derivations as they come in
	for drvPath := range visitChan {
		traverse(drvPath, drvs)
	}

	return drvs
}

func init() {
	// Import additional packages
	_ = bufio.NewScanner
	_ = json.Unmarshal
	_ = exec.Command
}

func main() {
	findAllFODs()
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
