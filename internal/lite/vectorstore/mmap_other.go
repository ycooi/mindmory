//go:build !darwin && !linux

package vectorstore

import (
	"fmt"
	"os"
)

// Non-Unix platforms use bounded ReadAt in Store.readRecord. This object
// deliberately exposes no whole-file byte slice.
type mappedFile struct{}

func mapReadOnly(_ *os.File, _ int) (*mappedFile, error) { return &mappedFile{}, nil }
func (m *mappedFile) bytes() []byte                      { return nil }
func (m *mappedFile) close() error                       { return nil }

var errNoMapping = fmt.Errorf("memory mapping unavailable")
