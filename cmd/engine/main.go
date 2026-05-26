package main

import (
	"fmt"
	"os"

	"columnar/columnar"
)

func main() {
	mm, closer, err := columnar.Mmap("data/trades.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open csv: %v\n", err)
		os.Exit(1)
	}
	defer closer()

	t, err := columnar.ParseCSV("crypto_trades", mm)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse csv: %v\n", err)
		os.Exit(1)
	}

	if err := columnar.SaveBinary(t, "db"); err != nil {
		fmt.Fprintf(os.Stderr, "save binary: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Table '%s' loaded and saved successfully!\n", t.Name)
}
