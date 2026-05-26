package columnar

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"sync"
	"unsafe"
)

type ParsedTable struct {
	Name      string
	NRows     uint64
	TradeID   []int32
	Symbol    []byte
	Price     []float64
	Quantity  []float64
	IsValid   []byte
}

type parseErr struct {
	localRow uint64
	msg      string
}

type chunkResult struct {
	idx        int
	totalLines uint64
	validRows  uint64
	tradeID    []int32
	symbol     []byte
	price      []float64
	quantity   []float64
	isValid    []byte
	errs       []parseErr
}

func ParseCSV(name string, mm []byte) (*ParsedTable, error) {
	nl := bytes.IndexByte(mm, '\n')
	if nl < 0 {
		return nil, fmt.Errorf("CSV has no header")
	}
	body := mm[nl+1:]
	if len(body) == 0 {
		return &ParsedTable{Name: name}, nil
	}

	workers := runtime.NumCPU()
	if len(body) < 1<<20 {
		workers = 1
	}
	chunks := splitByNewline(body, workers)

	results := make([]chunkResult, len(chunks))
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, data []byte) {
			defer wg.Done()
			results[idx] = parseChunk(idx, data)
		}(i, chunk)
	}
	wg.Wait()

	var totalValid, baseRow uint64
	for _, res := range results {
		for _, pErr := range res.errs {
			fmt.Fprintf(os.Stderr, "Row %d %s\n", baseRow+pErr.localRow, pErr.msg)
		}
		baseRow += res.totalLines
		totalValid += res.validRows
	}

	out := &ParsedTable{
		Name:     name,
		NRows:    totalValid,
		TradeID:  make([]int32, 0, totalValid),
		Symbol:   make([]byte, 0, totalValid*StringWidth),
		Price:    make([]float64, 0, totalValid),
		Quantity: make([]float64, 0, totalValid),
		IsValid:  make([]byte, 0, totalValid),
	}
	for _, res := range results {
		out.TradeID = append(out.TradeID, res.tradeID...)
		out.Symbol = append(out.Symbol, res.symbol...)
		out.Price = append(out.Price, res.price...)
		out.Quantity = append(out.Quantity, res.quantity...)
		out.IsValid = append(out.IsValid, res.isValid...)
	}
	return out, nil
}

func splitByNewline(data []byte, n int) [][]byte {
	if n <= 1 {
		return [][]byte{data}
	}
	out := make([][]byte, 0, n)
	prev := 0
	for i := 1; i < n; i++ {
		target := len(data) * i / n
		if target <= prev {
			continue
		}
		j := bytes.IndexByte(data[target:], '\n')
		if j < 0 {
			break
		}
		end := target + j + 1
		out = append(out, data[prev:end])
		prev = end
	}
	if prev < len(data) {
		out = append(out, data[prev:])
	}
	return out
}

func parseChunk(idx int, data []byte) chunkResult {
	estRows := len(data)/30 + 16
	res := chunkResult{
		idx:      idx,
		tradeID:  make([]int32, 0, estRows),
		symbol:   make([]byte, 0, estRows*StringWidth),
		price:    make([]float64, 0, estRows),
		quantity: make([]float64, 0, estRows),
		isValid:  make([]byte, 0, estRows),
	}

	pos := 0
	for pos < len(data) {
		nl := bytes.IndexByte(data[pos:], '\n')
		var line []byte
		if nl < 0 {
			line = data[pos:]
			pos = len(data)
		} else {
			line = data[pos : pos+nl]
			pos += nl + 1
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			continue
		}
		res.totalLines++

		if len(line) > MaxLineLen {
			res.errs = append(res.errs, parseErr{res.totalLines, "exceeds line buffer, skipping"})
			continue
		}

		var fields [5][]byte
		nFields, ok := splitFive(line, &fields)
		if !ok || nFields < 5 {
			res.errs = append(res.errs, parseErr{res.totalLines,
				fmt.Sprintf("has %d fields, expected 5, skipping", nFields)})
			continue
		}

		tid, ok := atoi32(fields[0])
		if !ok {
			res.errs = append(res.errs, parseErr{res.totalLines, "invalid Trade_ID, skipping"})
			continue
		}
		price, ok := atof64(fields[2])
		if !ok {
			res.errs = append(res.errs, parseErr{res.totalLines, "invalid Price, skipping"})
			continue
		}
		qty, ok := atof64(fields[3])
		if !ok {
			res.errs = append(res.errs, parseErr{res.totalLines, "invalid Quantity, skipping"})
			continue
		}
		valid, ok := atoBool(fields[4])
		if !ok {
			res.errs = append(res.errs, parseErr{res.totalLines, "invalid Is_Valid, skipping"})
			continue
		}

		res.tradeID = append(res.tradeID, tid)
		var sym [StringWidth]byte
		symLen := len(fields[1])
		if symLen > StringWidth-1 {
			symLen = StringWidth - 1
		}
		copy(sym[:symLen], fields[1][:symLen])
		res.symbol = append(res.symbol, sym[:]...)
		res.price = append(res.price, price)
		res.quantity = append(res.quantity, qty)
		res.isValid = append(res.isValid, valid)
		res.validRows++
	}
	return res
}

func splitFive(line []byte, out *[5][]byte) (int, bool) {
	n := 0
	start := 0
	for i := 0; i < len(line); i++ {
		if line[i] == ',' {
			if n >= 5 {
				return 5, true
			}
			out[n] = line[start:i]
			n++
			start = i + 1
		}
	}
	if n < 5 {
		out[n] = line[start:]
		n++
	}
	return n, n == 5
}

func atoi32(b []byte) (int32, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var val int32
	neg := false
	i := 0
	if b[0] == '-' {
		neg = true
		i = 1
	} else if b[0] == '+' {
		i = 1
	}
	if i >= len(b) {
		return 0, false
	}
	for ; i < len(b); i++ {
		ch := b[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		val = val*10 + int32(ch-'0')
	}
	if neg {
		val = -val
	}
	return val, true
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
		return strconvParseFloat(b)
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

func strconvParseFloat(b []byte) (float64, bool) {
	s := unsafe.String(unsafe.SliceData(b), len(b))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func atoBool(b []byte) (byte, bool) {
	if len(b) == 1 {
		switch b[0] {
		case '0':
			return 0, true
		case '1':
			return 1, true
		}
	}
	return 0, false
}
