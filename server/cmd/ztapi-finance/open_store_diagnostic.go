//go:build ztapi_store_diagnostic

package main

import (
	"fmt"
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func main() {
	db, err := gorm.Open(sqlite.Open(os.Getenv("FINANCE_STORE")), &gorm.Config{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "STORE_OPEN_ERROR:", err)
		os.Exit(1)
	}
	var result string
	if err := db.Raw("PRAGMA integrity_check").Scan(&result).Error; err != nil || result != "ok" {
		fmt.Fprintln(os.Stderr, "STORE_INTEGRITY_ERROR:", result, err)
		os.Exit(1)
	}
	fmt.Println("STORE_OPEN_OK")
}
