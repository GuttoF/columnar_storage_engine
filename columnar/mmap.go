package columnar

import (
	"os"
	"syscall"
)

func Mmap(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	size := int(st.Size())
	if size == 0 {
		f.Close()
		return nil, func() error { return nil }, nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	_ = syscall.Madvise(data, syscall.MADV_SEQUENTIAL)
	closer := func() error {
		err1 := syscall.Munmap(data)
		err2 := f.Close()
		if err1 != nil {
			return err1
		}
		return err2
	}
	return data, closer, nil
}
