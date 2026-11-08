//go:build linux

package sound

import (
	"os"
	"syscall"
)

func mapFile(path string) ([]byte, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > 64<<20 {
		return nil, nil, os.ErrInvalid
	}
	size := int(st.Size())
	data, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, err
	}
	unmap := func() {
		_ = syscall.Munmap(data)
	}
	return data, unmap, nil
}
