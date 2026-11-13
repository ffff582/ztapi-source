//go:build !windows

package main

import (
	"os"
	"sync"
	"syscall"
)

func lockStore(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }) }, nil
}
