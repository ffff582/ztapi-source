// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "support receipt import failed: use only the four SUPPORT_IMPORT_* environment variables")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	options := receiptImportOptions{ManifestPath: os.Getenv("SUPPORT_IMPORT_MANIFEST"), SHA256: os.Getenv("SUPPORT_IMPORT_SHA256"), OperatorID: os.Getenv("SUPPORT_IMPORT_OPERATOR_ID")}
	if err := runReceiptImport(ctx, options, os.Getenv("SUPPORT_IMPORT_STORE"), os.Stdout, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, "support receipt import failed:", err)
		os.Exit(1)
	}
}

func readImportManifest(path, pin string) (support.ReceiptImportManifest, error) {
	var manifest support.ReceiptImportManifest
	digest, err := hex.DecodeString(pin)
	if err != nil || len(pin) != 64 || strings.ToLower(pin) != pin || len(digest) != sha256.Size || !filepath.IsAbs(path) {
		return manifest, errors.New("absolute manifest and lowercase SHA256 pin required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return manifest, errors.New("manifest must be a protected regular file within 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest, errors.New("manifest unavailable")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.Mode().IsRegular() || !protectedManifest(file, actual) {
		return manifest, errors.New("manifest protection or identity check failed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return manifest, errors.New("manifest exceeds safe read limit")
	}
	hash := sha256.Sum256(raw)
	if !bytes.Equal(hash[:], digest) {
		return manifest, errors.New("manifest SHA256 mismatch")
	}
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), &manifest) != nil {
		return support.ReceiptImportManifest{}, errors.New("invalid receipt import manifest")
	}
	return manifest, nil
}

type importSchemaRow struct{ Type, Name, TableName, SQL string }

type receiptImportOptions struct{ ManifestPath, SHA256, OperatorID string }

func importSchema(db *gorm.DB) ([]importSchemaRow, error) {
	rows := []importSchemaRow{}
	err := db.Raw("SELECT type,name,tbl_name AS table_name,COALESCE(sql,'') AS sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name").Scan(&rows).Error
	return rows, err
}

func runReceiptImport(ctx context.Context, options receiptImportOptions, storePath string, out io.Writer, now time.Time) error {
	operator, err := strconv.Atoi(options.OperatorID)
	if err != nil || operator <= 0 || strconv.Itoa(operator) != options.OperatorID || !filepath.IsAbs(storePath) {
		return errors.New("absolute SUPPORT_IMPORT_STORE and explicit positive SUPPORT_IMPORT_OPERATOR_ID required")
	}
	manifest, err := readImportManifest(options.ManifestPath, options.SHA256)
	if err != nil {
		return err
	}
	info, err := os.Lstat(storePath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("existing regular support store required")
	}
	// Reject symlink aliases, including parent components, so the lease always
	// addresses the same store pathname as the independently stopped bot.
	canonical, err := filepath.EvalSymlinks(storePath)
	if err != nil || !sameImportPath(canonical, filepath.Clean(storePath)) {
		return errors.New("canonical support store path required")
	}
	unlock, err := lockStore(storePath + ".runtime.lock")
	if err != nil {
		return errors.New("support runtime lease unavailable; stop only the bot gracefully first")
	}
	defer unlock()
	current, err := os.Lstat(storePath)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return errors.New("support store identity changed")
	}
	filePath := filepath.ToSlash(storePath)
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	dsn := url.URL{Scheme: "file", Path: filePath, RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	db, err := gorm.Open(sqlite.Open(dsn.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return errors.New("existing support store unavailable")
	}
	raw, err := db.DB()
	if err != nil {
		return errors.New("support connection unavailable")
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	db = db.WithContext(ctx)
	var journal string
	if db.Raw("PRAGMA journal_mode").Scan(&journal).Error != nil || journal != "wal" {
		return errors.New("existing SQLite WAL support store required")
	}
	if db.Exec("PRAGMA busy_timeout=5000").Error != nil || db.Exec("PRAGMA synchronous=FULL").Error != nil {
		return errors.New("support durability configuration failed")
	}
	added := 0
	err = db.Transaction(func(tx *gorm.DB) error {
		before, err := importSchema(tx)
		if err != nil {
			return err
		}
		store, err := support.NewStore(tx)
		if err != nil {
			return err
		}
		after, err := importSchema(tx)
		if err != nil {
			return err
		}
		// NewStore is required, but this sidecar must never upgrade existing
		// support schemas. Any migration is rejected and rolled back here.
		if !reflect.DeepEqual(before, after) {
			return errors.New("existing support schema must not be migrated")
		}
		added, err = store.ImportCustomerServiceReceipts(ctx, manifest, operator, now)
		return err
	})
	if err != nil {
		if errors.Is(err, support.ErrConflict) {
			return errors.New("acceptance receipt validation or replay conflict; nothing imported")
		}
		return errors.New("receipt import transaction failed; nothing imported")
	}
	counts, err := common.Marshal(struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
		Total    int `json:"total"`
	}{added, len(manifest.Usages) - added, len(manifest.Usages)})
	if err == nil {
		_, err = fmt.Fprintln(out, string(counts))
	}
	if err != nil {
		return errors.New("receipt import committed; count output failed; identical replay is safe")
	}
	return nil
}
