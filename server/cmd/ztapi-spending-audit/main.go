// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/spending"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type snapshotReader interface {
	Load(context.Context, int, time.Time) (spending.Snapshot, error)
}

type auditReport struct {
	AsOf               time.Time `json:"as_of"`
	CheckedUsers       int       `json:"checked_users"`
	CompleteUsers      int       `json:"complete_users"`
	IncompleteUsers    int       `json:"incomplete_users"`
	UnavailableUsers   int       `json:"unavailable_users"`
	PendingRequests    int       `json:"pending_requests"`
	IncompleteRequests int       `json:"incomplete_requests"`
	PaidModelCalls     int       `json:"paid_model_calls"`
	Interrupted        bool      `json:"interrupted"`
	Sampled            bool      `json:"sampled"`
	SelectOnlyVerified bool      `json:"select_only_verified"`
}

func auditSnapshots(ctx context.Context, source snapshotReader, owners []int, now time.Time) auditReport {
	report := auditReport{AsOf: now.UTC()}
	for _, owner := range owners {
		if ctx.Err() != nil {
			report.Interrupted = true
			break
		}
		report.CheckedUsers++
		snap, err := source.Load(ctx, owner, now)
		if err != nil {
			report.UnavailableUsers++
			continue
		}
		if snap.Overview.PendingRequests < 0 || snap.Overview.IncompleteRequests < 0 {
			report.UnavailableUsers++
			continue
		}
		if !snap.Overview.Complete {
			report.IncompleteUsers++
			report.PendingRequests += snap.Overview.PendingRequests
			report.IncompleteRequests += snap.Overview.IncompleteRequests
			continue
		}
		valid := true
		for _, amount := range []*string{snap.Overview.ConfirmedDailyU, snap.Overview.ConfirmedHourlyU, snap.Overview.ConfirmedRefundsDailyU} {
			if amount == nil {
				valid = false
				break
			}
			d, e := decimal.NewFromString(*amount)
			if e != nil || d.IsNegative() {
				valid = false
				break
			}
		}
		if !valid {
			report.UnavailableUsers++
			continue
		}
		report.CompleteUsers++
		report.PendingRequests += snap.Overview.PendingRequests
		report.IncompleteRequests += snap.Overview.IncompleteRequests
	}
	return report
}

var selectGrant = regexp.MustCompile(`^GRANT SELECT(?: \([^;]+\))? ON `)

func selectOnlyGrants(grants []string) bool {
	selected := false
	for _, grant := range grants {
		grant = strings.ToUpper(strings.TrimSpace(grant))
		if strings.Contains(grant, "WITH GRANT OPTION") {
			return false
		}
		if strings.HasPrefix(grant, "GRANT USAGE ON ") {
			continue
		}
		if !selectGrant.MatchString(grant) {
			return false
		}
		selected = true
	}
	return selected
}

func run() (auditReport, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dsn := os.Getenv("SPENDING_READ_DSN")
	if dsn == "" || os.Getenv("SPENDING_QUOTA_PER_UNIT") != "500000" {
		return auditReport{}, errors.New("audit configuration unavailable")
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return auditReport{}, errors.New("audit configuration invalid")
	}
	cfg.ParseTime = true
	cfg.Loc = spending.Beijing
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 3*time.Second, 3*time.Second, 3*time.Second
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: cfg.FormatDSN(), SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return auditReport{}, errors.New("audit connection unavailable")
	}
	raw, err := db.DB()
	if err != nil {
		return auditReport{}, errors.New("audit connection unavailable")
	}
	defer raw.Close()
	raw.SetMaxOpenConns(2)
	raw.SetMaxIdleConns(1)
	grants, err := db.WithContext(ctx).Raw("SHOW GRANTS FOR CURRENT_USER()").Rows()
	if err != nil {
		return auditReport{}, errors.New("audit privilege evidence unavailable")
	}
	var permissions []string
	for grants.Next() {
		var line string
		if grants.Scan(&line) != nil {
			grants.Close()
			return auditReport{}, errors.New("audit privilege evidence invalid")
		}
		permissions = append(permissions, line)
	}
	readErr := grants.Err()
	grants.Close()
	if readErr != nil || !selectOnlyGrants(permissions) {
		return auditReport{}, errors.New("audit requires SELECT-only privileges")
	}
	reader := &spending.Reader{DB: db, VerifiedQuotaPerUnit: "500000"}
	if reader.Preflight(ctx) != nil {
		return auditReport{}, errors.New("audit source projection unavailable")
	}
	limit := 50
	if text := os.Getenv("SPENDING_AUDIT_LIMIT"); text != "" {
		limit, err = strconv.Atoi(text)
		if err != nil || limit < 1 || limit > 200 {
			return auditReport{}, errors.New("audit limit invalid")
		}
	}
	var owners []int
	if db.WithContext(ctx).Table("users").Where("status = ? AND deleted_at IS NULL", 1).Order("id").Limit(limit+1).Pluck("id", &owners).Error != nil {
		return auditReport{}, errors.New("audit owners unavailable")
	}
	sampled := len(owners) > limit
	if sampled {
		owners = owners[:limit]
	}
	report := auditSnapshots(ctx, reader, owners, time.Now())
	report.Sampled, report.SelectOnlyVerified = sampled, true
	return report, nil
}

func sourceColumnsJSON() ([]byte, error) {
	return common.Marshal(spending.RequiredSourceColumns())
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--source-columns" {
		raw, err := sourceColumnsJSON()
		if err != nil {
			fmt.Fprintln(os.Stderr, "spending source metadata unavailable")
			os.Exit(1)
		}
		fmt.Println(string(raw))
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "unsupported spending audit arguments")
		os.Exit(1)
	}
	report, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spending readonly audit unavailable")
		os.Exit(1)
	}
	raw, err := common.Marshal(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spending readonly audit serialization failed")
		os.Exit(1)
	}
	fmt.Println(string(raw))
	if report.Interrupted || report.UnavailableUsers > 0 || report.IncompleteUsers > 0 {
		os.Exit(2)
	}
}
