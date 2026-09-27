//go:build windows

package engine

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func preallocate(file *os.File, size int64) error {
	handle := windows.Handle(file.Fd())
	high := int32(size >> 32)
	if _, err := windows.SetFilePointer(handle, int32(size), &high, windows.FILE_BEGIN); err != nil {
		return fmt.Errorf("seek partial file for preallocation: %w", err)
	}
	if err := windows.SetEndOfFile(handle); err != nil {
		return fmt.Errorf("preallocate partial file: %w", err)
	}
	return nil
}
