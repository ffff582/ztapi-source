//go:build !windows

package main

import "os"

func protectedManifest(_ *os.File, info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }
func sameImportPath(a, b string) bool                     { return a == b }
