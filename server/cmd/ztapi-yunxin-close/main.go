package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	mode := flag.String("mode", "", "preview or apply")
	operator := flag.Int("operator", 0, "authorizing finance operator ID")
	flag.Parse()
	if flag.NArg() != 0 || (*mode != "preview" && *mode != "apply") || *operator <= 0 || os.Getenv("SQL_DSN") == "" {
		fmt.Fprintln(os.Stderr, "explicit mode, operator and database configuration required")
		os.Exit(2)
	}
	if *mode == "apply" {
		if os.Getenv("REDIS_CONN_STRING") == "" || common.InitRedisClient() != nil {
			fmt.Fprintln(os.Stderr, "shared cache unavailable; no settlement attempted")
			os.Exit(1)
		}
	}
	db, err := gorm.Open(mysql.Open(os.Getenv("SQL_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connection unavailable")
		os.Exit(1)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connection unavailable")
		os.Exit(1)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	model.DB = db
	receipts, err := model.ApplyZTAPIYunxinClose(*operator, *mode == "apply", service.PriceZTAPIAttemptBilling)
	if err != nil {
		fmt.Fprintln(os.Stderr, "closure not committed:", common.RedactSecrets(err.Error()))
		os.Exit(1)
	}
	raw, err := common.Marshal(receipts)
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(string(raw))
}
