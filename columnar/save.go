package columnar

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

func SaveBinary(t *ParsedTable, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "columns"), 0o755); err != nil {
		return err
	}
	if err := saveMetadata(t, filepath.Join(dir, "metadata.bin")); err != nil {
		return err
	}
	for _, col := range DefaultSchema {
		path := filepath.Join(dir, "columns", col.Name+".bin")
		if err := saveColumn(t, col, path); err != nil {
			return err
		}
	}
	return nil
}

func saveMetadata(t *ParsedTable, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	nCols := uint64(len(DefaultSchema))
	if err := binary.Write(f, binary.LittleEndian, nCols); err != nil {
		return err
	}
	for _, col := range DefaultSchema {
		var name [64]byte
		copy(name[:], col.Name)
		if _, err := f.Write(name[:]); err != nil {
			return err
		}
		if err := binary.Write(f, binary.LittleEndian, uint32(col.Type)); err != nil {
			return err
		}
		if err := binary.Write(f, binary.LittleEndian, t.NRows); err != nil {
			return err
		}
	}
	return nil
}

func saveColumn(t *ParsedTable, col ColumnSchema, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var raw []byte
	switch col.Name {
	case "Trade_ID":
		raw = asBytes(unsafe.Pointer(unsafe.SliceData(t.TradeID)), len(t.TradeID)*4)
	case "Symbol":
		raw = t.Symbol
	case "Price":
		raw = asBytes(unsafe.Pointer(unsafe.SliceData(t.Price)), len(t.Price)*8)
	case "Quantity":
		raw = asBytes(unsafe.Pointer(unsafe.SliceData(t.Quantity)), len(t.Quantity)*8)
	case "Is_Valid":
		raw = t.IsValid
	default:
		return fmt.Errorf("unknown column %q", col.Name)
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	return nil
}

func asBytes(ptr unsafe.Pointer, n int) []byte {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(ptr), n)
}
