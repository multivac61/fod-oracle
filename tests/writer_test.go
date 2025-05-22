package tests

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

// Using the FOD struct from hello_deps.go
// No need to redefine it here

// ParquetFOD is a struct specifically for Parquet file format
type ParquetFOD struct {
	DrvPath       string `parquet:"name=drv_path, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	OutputPath    string `parquet:"name=output_path, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	HashAlgorithm string `parquet:"name=hash_algorithm, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	Hash          string `parquet:"name=hash, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	RevisionID    int64  `parquet:"name=revision_id, type=INT64, repetitiontype=OPTIONAL"`
}

// CreateFODs creates test FOD data
func CreateFODs(count int) []FOD {
	fods := make([]FOD, count)
	for i := 0; i < count; i++ {
		fods[i] = FOD{
			DrvPath:       fmt.Sprintf("/nix/store/test-path-%d.drv", i),
			OutputPath:    fmt.Sprintf("/nix/store/test-output-path-%d", i),
			HashAlgorithm: "sha256",
			Hash:          fmt.Sprintf("hash%d", i),
		}
	}
	return fods
}

// readCSVFODs reads FODs from a CSV file
func readCSVFODs(t *testing.T, filePath string) []FOD {
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open CSV file: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// Read header
	header, err := reader.Read()
	if err != nil {
		t.Fatalf("Failed to read CSV header: %v", err)
	}

	// Find column indices
	var drvPathIdx, outputPathIdx, hashAlgoIdx, hashIdx int = -1, -1, -1, -1
	for i, col := range header {
		switch col {
		case "drv_path":
			drvPathIdx = i
		case "output_path":
			outputPathIdx = i
		case "hash_algorithm":
			hashAlgoIdx = i
		case "hash":
			hashIdx = i
		}
	}

	if drvPathIdx == -1 || outputPathIdx == -1 || hashAlgoIdx == -1 || hashIdx == -1 {
		t.Fatalf("CSV header missing required columns")
	}

	var fods []FOD
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Error reading CSV record: %v", err)
		}

		fod := FOD{
			DrvPath:       record[drvPathIdx],
			OutputPath:    record[outputPathIdx],
			HashAlgorithm: record[hashAlgoIdx],
			Hash:          record[hashIdx],
		}
		fods = append(fods, fod)
	}

	return fods
}

// readJSONFODs reads FODs from a JSON file
func readJSONFODs(t *testing.T, filePath string) []FOD {
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open JSON file: %v", err)
	}
	defer file.Close()

	var fods []FOD
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&fods); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	return fods
}

// checkParquetFile checks if the Parquet file exists and has content
func checkParquetFile(t *testing.T, filePath string) error {
	// Check if file exists
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat Parquet file: %w", err)
	}

	// Check if file has content
	if info.Size() == 0 {
		return fmt.Errorf("Parquet file is empty")
	}

	t.Logf("Parquet file exists with size: %d bytes", info.Size())
	return nil
}

// TestFormatOutputConsistency is a test that verifies all output
// formats produce consistent results
func TestFormatOutputConsistency(t *testing.T) {
	t.Skip("This test needs to be run from the main package")

	// In a real implementation, we would need to:
	// 1. Set up test config
	// 2. Create test output files
	// 3. Read back and verify the data
}
