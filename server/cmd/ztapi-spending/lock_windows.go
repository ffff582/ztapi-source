//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"os"
	"sync"
)

func lockStore(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(file.Fd())
	overlap := &windows.Overlapped{}
	if err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlap); err != nil {
		_ = file.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, overlap); _ = file.Close() }) }, nil
}
