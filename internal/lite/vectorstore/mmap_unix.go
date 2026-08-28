//go:build darwin || linux

package vectorstore

import (
	"golang.org/x/sys/unix"
	"os"
)

type mappedFile struct{ data []byte }

func mapReadOnly(file *os.File, size int) (*mappedFile, error) {
	if size == 0 {
		return &mappedFile{}, nil
	}
	b, err := unix.Mmap(int(file.Fd()), 0, size, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &mappedFile{data: b}, nil
}
func (m *mappedFile) bytes() []byte { return m.data }
func (m *mappedFile) close() error {
	if len(m.data) == 0 {
		return nil
	}
	err := unix.Munmap(m.data)
	m.data = nil
	return err
}
