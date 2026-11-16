package main

import (
	"errors"
	"os"
	"path/filepath"
)

func storageHeadroom(used int64, available uint64) error {
	if used >= 960<<20 || available < 1<<30 {
		return errors.New("support storage headroom exhausted")
	}
	return nil
}

func storageAdmission(path string) func() error {
	return func() error {
		var size int64
		for _, suffix := range []string{"", "-wal", "-shm"} {
			info, err := os.Stat(path + suffix)
			if err != nil && !os.IsNotExist(err) {
				return errors.New("support storage unavailable")
			}
			if err == nil {
				if !info.Mode().IsRegular() {
					return errors.New("invalid support storage")
				}
				size += info.Size()
			}
		}
		free, err := freeStorage(filepath.Dir(path))
		if err != nil {
			return errors.New("support storage availability unknown")
		}
		return storageHeadroom(size, free)
	}
}
