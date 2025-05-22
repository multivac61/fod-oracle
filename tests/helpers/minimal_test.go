// minimal_test.go - Simulates the format writers for testing
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Println("Usage: minimal_test <csv-path> <json-path> <parquet-path>")
		os.Exit(1)
	}

	csvPath := os.Args[1]
	jsonPath := os.Args[2]
	parquetPath := os.Args[3]

	// Create CSV file
	fmt.Println("Creating CSV file...")
	csvContent := "drv_path,output_path,hash_algorithm,hash\n"
	for i := 0; i < 10; i++ {
		csvContent += fmt.Sprintf("/nix/store/test-path-%d.drv,/nix/store/test-output-path-%d,sha256,hash%d\n", i, i, i)
	}
	os.WriteFile(csvPath, []byte(csvContent), 0o644)

	// Create JSON file
	fmt.Println("Creating JSON file...")
	jsonContent := "[\n"
	for i := 0; i < 10; i++ {
		comma := ","
		if i == 9 {
			comma = ""
		}
		jsonContent += fmt.Sprintf("  {\"DrvPath\":\"/nix/store/test-path-%d.drv\",\"OutputPath\":\"/nix/store/test-output-path-%d\",\"HashAlgorithm\":\"sha256\",\"Hash\":\"hash%d\"}%s\n",
			i, i, i, comma)
	}
	jsonContent += "]\n"
	os.WriteFile(jsonPath, []byte(jsonContent), 0o644)

	// Create Parquet file (simulated as binary)
	fmt.Println("Creating Parquet file...")
	// Create a binary file with some content (not real parquet but good enough for test)
	parquetContent := []byte{0x50, 0x41, 0x52, 0x31} // "PAR1" magic bytes
	for i := 0; i < 1000; i++ {
		parquetContent = append(parquetContent, byte(i%256))
	}
	os.WriteFile(parquetPath, parquetContent, 0o644)

	fmt.Println("All files created successfully!")
}
