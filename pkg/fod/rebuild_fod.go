package fod

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// RebuildFODResult represents the result of rebuilding a fixed-output derivation
type RebuildFODResult struct {
	// Input derivation path
	DrvPath string
	// Status of the rebuild process (success, failure, etc.)
	Status string
	// Expected hash from the derivation
	ExpectedHash string
	// Actual hash found after the rebuild
	ActualHash string
	// Full output log from the rebuild process
	Log string
	// Error message if any
	ErrorMessage string
	// Whether a hash mismatch was detected
	HashMismatch bool
	// Timestamp when the rebuild was completed
	FinishedAt time.Time
}

// Method specific hash results
type hashResult struct {
	method string
	hash   string
}

// RebuildFOD rebuilds a fixed-output derivation and extracts the hash
func RebuildFOD(ctx context.Context, drvPath string) (*RebuildFODResult, error) {
	if drvPath == "" {
		return nil, errors.New("derivation path cannot be empty")
	}

	startTime := time.Now()
	var output bytes.Buffer

	// Initialize the result
	result := &RebuildFODResult{
		DrvPath:    drvPath,
		Status:     "failure", // Default to failure, will change to success if everything works
		FinishedAt: time.Now(),
	}

	fmt.Fprintf(&output, "🔄 Processing derivation: %s\n", drvPath)

	// Track all hash methods
	hashes := make(map[string]string)
	var bestHash string
	var rawOutputHash string

	// METHOD 1: Extract hash from nix show-derivation JSON output
	fmt.Fprintf(&output, "\n1️⃣ Getting hash from derivation JSON...\n")
	hash1, err := getHashFromJSON(drvPath, &output)
	if err != nil {
		fmt.Fprintf(&output, "💠 [Method 1] Error: %v\n", err)
	} else if hash1 != "" {
		hashes["Method 1 (JSON)"] = hash1
		rawOutputHash = hash1
		fmt.Fprintf(&output, "💠 [Method 1] Hash (hex format): %s\n", hash1)
	} else {
		fmt.Fprintf(&output, "💠 [Method 1] No valid hash found in derivation JSON.\n")
	}

	// METHOD 2: Direct query with nix-store -q command
	fmt.Fprintf(&output, "\n2️⃣ Getting hash using nix-store query...\n")
	hash2, err := getHashFromNixStore(drvPath, &output)
	if err != nil {
		fmt.Fprintf(&output, "🔷 [Method 2] Error: %v\n", err)
	} else if hash2 != "" {
		hashes["Method 2 (Query)"] = hash2
		if rawOutputHash == "" {
			rawOutputHash = hash2
		}
		fmt.Fprintf(&output, "🔷 [Method 2] Hash (hex format): %s\n", hash2)
	} else {
		fmt.Fprintf(&output, "🔷 [Method 2] No valid hash found via nix-store query.\n")
	}

	// METHOD 3: Get output path and compute its hash directly
	fmt.Fprintf(&output, "\n3️⃣ Computing hash from output file...\n")
	outputPath, hash3, err := getHashFromOutputPath(ctx, drvPath, &output)
	if err != nil {
		fmt.Fprintf(&output, "🟦 [Method 3] Error: %v\n", err)
	} else if hash3 != "" {
		hashes["Method 3 (Computed)"] = hash3
		fmt.Fprintf(&output, "🟦 [Method 3] Hash (hex, computed from output): %s\n", hash3)
	}

	// METHOD 4: Only used if previous methods failed to provide sufficient info
	needsBuild := true
	if len(hashes) > 0 || (outputPath != "" && fileExists(outputPath)) {
		needsBuild = false
		fmt.Fprintf(&output, "\n4️⃣ Skipping build attempt as we already have a hash from a prior method.\n")
	} else {
		fmt.Fprintf(&output, "\n4️⃣ No hash found yet, will try building...\n")
	}

	if needsBuild {
		fmt.Fprintf(&output, "4️⃣ No fixed outputHash or existing outputPath found yet, attempting to build %s...\n", drvPath)
		expectedHash, actualHash, buildOutput, err := attemptBuildAndExtractHash(ctx, drvPath)
		fmt.Fprintf(&output, "%s\n", buildOutput)

		if err != nil {
			fmt.Fprintf(&output, "⚠️ Build failed: %v\n", err)
		} else {
			if expectedHash != "" {
				hashes["Method 4 (Build Wanted)"] = expectedHash
				fmt.Fprintf(&output, "⬛ [Method 4] Expected hash (hex format): %s\n", expectedHash)
			}
			if actualHash != "" {
				hashes["Method 4 (Build Got)"] = actualHash
				fmt.Fprintf(&output, "⬛ [Method 4] Actual hash (hex format): %s\n", actualHash)
			}

			// If we got a hash mismatch during build
			if expectedHash != "" && actualHash != "" && expectedHash != actualHash {
				result.HashMismatch = true
				result.ExpectedHash = expectedHash
				result.ActualHash = actualHash
				fmt.Fprintf(&output, "⚠️ Hash mismatch detected during build.\n")
			}
		}

		// Also try to compute hash from output if it's now available
		if outputPath != "" && fileExists(outputPath) {
			computedHash, err := computeHashFromPath(outputPath)
			if err == nil && computedHash != "" {
				hashes["Method 4 (Build Computed)"] = computedHash
				fmt.Fprintf(&output, "⬛ [Method 4] Hash (hex, computed from built output): %s\n", computedHash)
			}
		}
	}

	// Summary of results
	fmt.Fprintf(&output, "\n📊 SUMMARY OF HEX HASHES 📊\n")
	fmt.Fprintf(&output, "====================================\n")

	// Find the best hash - prioritize method 3 (computed) first
	hashPriorities := []string{
		"Method 3 (Computed)",
		"Method 2 (Query)",
		"Method 1 (JSON)",
		"Method 4 (Build Computed)",
		"Method 4 (Build Got)",
		"Method 4 (Build Wanted)",
	}

	// Find the best hash based on priority
	for _, method := range hashPriorities {
		if hash, ok := hashes[method]; ok && hash != "" {
			bestHash = hash
			result.ActualHash = hash
			fmt.Fprintf(&output, "🔑 BEST HASH (%s):\n", method)
			fmt.Fprintf(&output, "%s\n\n", hash)
			fmt.Fprintf(&output, "All available hashes:\n")
			break
		}
	}

	// If we have a hash, consider it a success
	if bestHash != "" {
		result.Status = "success"
	}

	// Show all available hashes
	if len(hashes) > 0 {
		for method, hash := range hashes {
			fmt.Fprintf(&output, "%-25s %s\n", method+":", hash)
		}
	} else {
		fmt.Fprintf(&output, "No hex hash could be determined through any method for %s.\n", drvPath)
	}

	result.Log = output.String()
	result.FinishedAt = time.Now()

	// Log performance information
	totalDuration := time.Since(startTime)
	if totalDuration > 1*time.Second {
		fmt.Fprintf(os.Stderr, "rebuild-fod took %v to complete for %s\n", totalDuration, drvPath)
	}

	return result, nil
}

// getHashFromJSON extracts hash from nix show-derivation JSON output
func getHashFromJSON(drvPath string, output *bytes.Buffer) (string, error) {
	cmd := exec.Command("nix", "show-derivation", drvPath)
	jsonOutput, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute nix show-derivation: %w", err)
	}

	if len(jsonOutput) == 0 {
		return "", nil
	}

	var drvData map[string]map[string]interface{}
	if err := json.Unmarshal(jsonOutput, &drvData); err != nil {
		return "", fmt.Errorf("failed to parse derivation JSON: %w", err)
	}

	// Get the derivation data
	drv, ok := drvData[drvPath]
	if !ok {
		return "", fmt.Errorf("derivation path not found in JSON output")
	}

	// Extract outputHash
	outputHash, ok := drv["outputHash"].(string)
	if !ok || outputHash == "" {
		return "", nil
	}

	// Extract algorithm and mode
	outputHashAlgo, _ := drv["outputHashAlgo"].(string)
	if outputHashAlgo == "" {
		outputHashAlgo = "sha256"
	}
	outputHashMode, _ := drv["outputHashMode"].(string)
	if outputHashMode == "" {
		outputHashMode = "recursive"
	}

	fmt.Fprintf(output, "💠 [Method 1] Raw hash from JSON: %s (Algo: %s, Mode: %s)\n",
		outputHash, outputHashAlgo, outputHashMode)

	// Try to convert to hex format
	hexHash, err := convertToHex(outputHash, outputHashAlgo)
	if err != nil {
		return "", fmt.Errorf("failed to convert hash to hex: %w", err)
	}

	return hexHash, nil
}

// getHashFromNixStore extracts hash using nix-store -q command
func getHashFromNixStore(drvPath string, output *bytes.Buffer) (string, error) {
	cmd := exec.Command("nix-store", "-q", "--binding", "outputHash", drvPath)
	rawHash, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// Normal for non-fixed-output derivations
			return "", nil
		}
		return "", fmt.Errorf("failed to execute nix-store query: %w", err)
	}

	hashStr := strings.TrimSpace(string(rawHash))
	if hashStr == "" {
		return "", nil
	}

	// Get algorithm and mode
	algoCmd := exec.Command("nix-store", "-q", "--binding", "outputHashAlgo", drvPath)
	algoOutput, _ := algoCmd.Output()
	algo := strings.TrimSpace(string(algoOutput))
	if algo == "" {
		algo = "sha256"
	}

	modeCmd := exec.Command("nix-store", "-q", "--binding", "outputHashMode", drvPath)
	modeOutput, _ := modeCmd.Output()
	mode := strings.TrimSpace(string(modeOutput))
	if mode == "" {
		mode = "recursive"
	}

	fmt.Fprintf(output, "🔷 [Method 2] Raw hash from nix-store: %s (Algo: %s, Mode: %s)\n",
		hashStr, algo, mode)

	// Try to convert to hex format
	hexHash, err := convertToHex(hashStr, algo)
	if err != nil {
		return "", fmt.Errorf("failed to convert hash to hex: %w", err)
	}

	return hexHash, nil
}

// getHashFromOutputPath gets output path and computes its hash directly
func getHashFromOutputPath(ctx context.Context, drvPath string, output *bytes.Buffer) (string, string, error) {
	cmd := exec.CommandContext(ctx, "nix-store", "-q", "--outputs", drvPath)
	outputPathBytes, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to get output path: %w", err)
	}

	outputPath := strings.TrimSpace(string(outputPathBytes))
	if outputPath == "" {
		return "", "", fmt.Errorf("empty output path")
	}

	// Check if the output path exists
	if !fileExists(outputPath) {
		fmt.Fprintf(output, "📦 Output path %s does not exist in store. Build may be required.\n", outputPath)

		// Try to build it without substitutes
		fmt.Fprintf(output, "🏗️ Attempting to build %s without substitutes...\n", drvPath)
		buildCmd := exec.CommandContext(ctx, "nix-build", "--no-out-link", "--no-substitute", drvPath)
		if buildOutput, err := buildCmd.CombinedOutput(); err != nil {
			fmt.Fprintf(output, "❌ Build attempt failed: %v\n", err)
			fmt.Fprintf(output, "Build output: %s\n", string(buildOutput))
			return outputPath, "", fmt.Errorf("build failed: %w", err)
		}

		// Check if it exists now
		if !fileExists(outputPath) {
			return outputPath, "", fmt.Errorf("output still doesn't exist after build")
		}

		fmt.Fprintf(output, "✅ Build successful. Computing hash of new output...\n")
	} else {
		fmt.Fprintf(output, "📦 Output path: %s\n", outputPath)
	}

	// Compute hash
	hash, err := computeHashFromPath(outputPath)
	if err != nil {
		return outputPath, "", fmt.Errorf("failed to compute hash: %w", err)
	}

	return outputPath, hash, nil
}

// attemptBuildAndExtractHash tries to build the derivation and extract hash information
func attemptBuildAndExtractHash(ctx context.Context, drvPath string) (string, string, string, error) {
	var output bytes.Buffer

	cmd := exec.CommandContext(ctx, "nix-build", "--no-out-link", drvPath)
	buildOutput, err := cmd.CombinedOutput()
	output.Write(buildOutput)

	// Extract hashes from build output if there was a hash mismatch
	expectedHash := ""
	actualHash := ""

	if err != nil {
		// Look for hash mismatch pattern
		if strings.Contains(string(buildOutput), "hash mismatch") {
			// Extract expected hash
			expectedRegex := regexp.MustCompile(`wanted:.*hash '([^']*)'`)
			expectedMatches := expectedRegex.FindStringSubmatch(string(buildOutput))
			if len(expectedMatches) >= 2 {
				sriHash := expectedMatches[1]
				hexHash, err := convertToHex(sriHash, "")
				if err == nil {
					expectedHash = hexHash
				}
			}

			// Extract actual hash
			actualRegex := regexp.MustCompile(`got:.*hash '([^']*)'`)
			actualMatches := actualRegex.FindStringSubmatch(string(buildOutput))
			if len(actualMatches) >= 2 {
				sriHash := actualMatches[1]
				hexHash, err := convertToHex(sriHash, "")
				if err == nil {
					actualHash = hexHash
				}
			}
		}

		return expectedHash, actualHash, output.String(), fmt.Errorf("build failed: %w", err)
	}

	// If build succeeded, try to extract the output path
	pathRegex := regexp.MustCompile(`(?m)^(/nix/store/[a-z0-9]{32}-[^/\n]+)$`)
	pathMatches := pathRegex.FindStringSubmatch(string(buildOutput))
	if len(pathMatches) >= 2 {
		builtPath := pathMatches[1]
		// Verify it's actually an output of the derivation
		checkCmd := exec.CommandContext(ctx, "nix-store", "-q", "--outputs", drvPath)
		checkOutputs, err := checkCmd.Output()
		if err == nil {
			outputs := strings.Split(strings.TrimSpace(string(checkOutputs)), "\n")
			for _, output := range outputs {
				if output == builtPath {
					// Compute hash from the built output
					hash, err := computeHashFromPath(builtPath)
					if err == nil {
						actualHash = hash
					}
					break
				}
			}
		}
	}

	return expectedHash, actualHash, output.String(), nil
}

// computeHashFromPath computes the SHA-256 hash of a file or directory
func computeHashFromPath(path string) (string, error) {
	cmd := exec.Command("nix-hash", "--type", "sha256", "--flat", path)
	hashBytes, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to compute hash: %w", err)
	}

	return strings.TrimSpace(string(hashBytes)), nil
}

// convertToHex converts various hash formats to hex
func convertToHex(hashInput string, algo string) (string, error) {
	hashInput = strings.TrimSpace(hashInput)
	if hashInput == "" {
		return "", fmt.Errorf("empty hash input")
	}

	// Check if it's already hex (at least 32 characters, only hex chars)
	hexPattern := regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)
	if hexPattern.MatchString(hashInput) {
		return hashInput, nil
	}

	// Normalize SRI format
	hashInput = strings.ReplaceAll(hashInput, "sha1:", "sha1-")
	hashInput = strings.ReplaceAll(hashInput, "sha256:", "sha256-")
	hashInput = strings.ReplaceAll(hashInput, "sha512:", "sha512-")

	// Extract base64 part from SRI format
	var base64Part string
	if strings.HasPrefix(hashInput, "sha256-") {
		base64Part = strings.TrimPrefix(hashInput, "sha256-")
	} else if strings.HasPrefix(hashInput, "sha512-") {
		base64Part = strings.TrimPrefix(hashInput, "sha512-")
	} else if strings.HasPrefix(hashInput, "sha1-") {
		base64Part = strings.TrimPrefix(hashInput, "sha1-")
	} else if strings.Contains(hashInput, "=") || strings.Contains(hashInput, "/") || strings.Contains(hashInput, "+") {
		// Looks like base64
		base64Part = hashInput
	} else {
		// Try nix-hash conversion
		cmd := exec.Command("nix-hash", "--to-sri", algo, hashInput)
		sriBytes, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to convert hash with nix-hash: %w", err)
		}

		sriHash := strings.TrimSpace(string(sriBytes))
		return convertToHex(sriHash, algo)
	}

	if base64Part == "" {
		return "", fmt.Errorf("failed to extract base64 part from hash")
	}

	// Convert base64 to binary then to hex
	data, err := base64.StdEncoding.DecodeString(base64Part)
	if err != nil {
		// Try using base64url decoding if standard fails
		data, err = base64.RawURLEncoding.DecodeString(base64Part)
		if err != nil {
			return "", fmt.Errorf("failed to decode base64: %w", err)
		}
	}

	return hex.EncodeToString(data), nil
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
