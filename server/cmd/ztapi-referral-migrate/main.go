package main

import (
	"fmt"
	"os"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "--apply-new-referral-tables" || os.Getenv("SQL_DSN") == "" {
		fmt.Fprintln(os.Stderr, "explicit referral-only migration flag and database configuration required")
		os.Exit(2)
	}
	db, err := gorm.Open(mysql.Open(os.Getenv("SQL_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connection unavailable")
		os.Exit(1)
	}
	connection, err := db.DB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connection unavailable")
		os.Exit(1)
	}
	defer connection.Close()
	connection.SetMaxOpenConns(1)
	if err := model.MigrateZTAPIReferral(db); err != nil {
		fmt.Fprintln(os.Stderr, "referral-only migration failed; existing business data was not restored or removed")
		os.Exit(1)
	}
	fmt.Printf("referral-only schema verified: %d tables; activity remains unchanged\n", len(model.ZTAPIReferralModels()))
}
