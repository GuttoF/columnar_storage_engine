package main

import (
	"bytes"
	"fmt"
	"os"
	"time"
	"unsafe"

	"columnar/columnar"
)

func binaryAverage(path string) (float64, error) {
	mm, closer, err := columnar.Mmap(path)
	if err != nil {
		return 0, err
	}
	defer closer()
	if len(mm)%8 != 0 || len(mm) == 0 {
		return 0, fmt.Errorf("binary file size not a multiple of 8")
	}
	n := len(mm) / 8
	prices := unsafe.Slice((*float64)(unsafe.Pointer(unsafe.SliceData(mm))), n)
	var sum float64
	for _, price := range prices {
		sum += price
	}
	return sum / float64(n), nil
}

func csvAverage(path string) (float64, error) {
	mm, closer, err := columnar.Mmap(path)
	if err != nil {
		return 0, err
	}
	defer closer()

	nl := bytes.IndexByte(mm, '\n')
	if nl < 0 {
		return 0, fmt.Errorf("no header")
	}
	body := mm[nl+1:]

	var sum float64
	var n uint64
	var rowNum uint64
	pos := 0
	for pos < len(body) {
		idx := bytes.IndexByte(body[pos:], '\n')
		var line []byte
		if idx < 0 {
			line = body[pos:]
			pos = len(body)
		} else {
			line = body[pos : pos+idx]
			pos += idx + 1
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			continue
		}
		rowNum++
		if len(line) > columnar.MaxLineLen {
			fmt.Fprintf(os.Stderr, "Row %d exceeds line buffer, skipping\n", rowNum)
			continue
		}

		// Field 2 (zero-indexed) is Price. Scan to third comma.
		commas := 0
		start := 0
		end := -1
		for i := 0; i < len(line); i++ {
			if line[i] == ',' {
				commas++
				if commas == 2 {
					start = i + 1
				} else if commas == 3 {
					end = i
					break
				}
			}
		}
		if end < 0 {
			continue
		}
		v, ok := atof64(line[start:end])
		if !ok {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("no rows")
	}
	return sum / float64(n), nil
}

func atof64(b []byte) (float64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	i := 0
	neg := false
	if b[0] == '-' {
		neg = true
		i = 1
	} else if b[0] == '+' {
		i = 1
	}
	if i >= len(b) {
		return 0, false
	}
	var intPart uint64
	startInt := i
	for ; i < len(b); i++ {
		ch := b[i]
		if ch < '0' || ch > '9' {
			break
		}
		intPart = intPart*10 + uint64(ch-'0')
	}
	if i == startInt {
		return 0, false
	}
	var fracPart uint64
	var fracDigits int
	if i < len(b) && b[i] == '.' {
		i++
		for ; i < len(b); i++ {
			ch := b[i]
			if ch < '0' || ch > '9' {
				break
			}
			fracPart = fracPart*10 + uint64(ch-'0')
			fracDigits++
		}
	}
	if i < len(b) {
		return 0, false
	}
	v := float64(intPart)
	if fracDigits > 0 {
		v += float64(fracPart) * pow10neg[fracDigits]
	}
	if neg {
		v = -v
	}
	return v, true
}

var pow10neg = [...]float64{
	1,
	1e-1, 1e-2, 1e-3, 1e-4, 1e-5, 1e-6, 1e-7, 1e-8, 1e-9,
	1e-10, 1e-11, 1e-12, 1e-13, 1e-14, 1e-15, 1e-16, 1e-17, 1e-18, 1e-19,
}

func main() {
	const binaryPath = "db/columns/Price.bin"
	const csvPath = "data/trades.csv"

	startBin := time.Now()
	binAvg, err := binaryAverage(binaryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "binary read: %v\n", err)
		os.Exit(1)
	}
	binTime := time.Since(startBin).Seconds()

	startCSV := time.Now()
	csvAvg, err := csvAverage(csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csv read: %v\n", err)
		os.Exit(1)
	}
	csvTime := time.Since(startCSV).Seconds()

	fmt.Println("--- Benchmark Results ---")
	fmt.Printf("CSV Time:    %.3f seconds (Avg: $%.2f)\n", csvTime, csvAvg)
	fmt.Printf("Binary Time: %.3f seconds (Avg: $%.2f)\n", binTime, binAvg)
	fmt.Println("-------------------------")
	if binTime > 0 && csvTime > 0 {
		speed := csvTime / binTime
		if speed < 1 {
			fmt.Printf("The binary reader is %.1f times slower!\n", 1.0/speed)
		} else {
			fmt.Printf("The binary reader is %.1f times faster!\n", speed)
		}
		perc := (csvTime - binTime) / csvTime * 100
		if perc < 0 {
			fmt.Printf("Net time increased by %.1f%%\n", -perc)
		} else {
			fmt.Printf("Net time reduced by %.1f%%\n", perc)
		}
	}
}
