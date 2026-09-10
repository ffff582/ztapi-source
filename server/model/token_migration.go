package model

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type legacyTokenKeyColumn struct {
	Id  int
	Key string `gorm:"column:key"`
}

func (legacyTokenKeyColumn) TableName() string {
	return "tokens"
}

func tokenTableHasColumn(columnName string) (bool, error) {
	columnTypes, err := DB.Migrator().ColumnTypes("tokens")
	if err != nil {
		return false, err
	}
	for _, columnType := range columnTypes {
		if strings.EqualFold(columnType.Name(), columnName) {
			return true, nil
		}
	}
	return false, nil
}

func legacyTokenIDsQuery(db *gorm.DB) *gorm.DB {
	keyColumn := clause.Column{Name: "key"}
	return db.Model(&legacyTokenKeyColumn{}).Where(clause.And(
		clause.Neq{Column: keyColumn, Value: nil},
		clause.Neq{Column: keyColumn, Value: ""},
		clause.Not(clause.Like{Column: keyColumn, Value: "ztapi-invalid-legacy-token:%"}),
	))
}

func legacyTokenCaseExpression(ids []int, value func(int) string) (string, []any) {
	var expression strings.Builder
	expression.WriteString("CASE id")
	args := make([]any, 0, len(ids)*2)
	for _, id := range ids {
		expression.WriteString(" WHEN ? THEN ?")
		args = append(args, id, value(id))
	}
	expression.WriteString(" END")
	return expression.String(), args
}

func invalidateLegacyTokenBatch(db *gorm.DB, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	keyExpression, keyArgs := legacyTokenCaseExpression(ids, func(id int) string {
		return fmt.Sprintf("ztapi-invalid-legacy-token:%d", id)
	})
	hashExpression, hashArgs := legacyTokenCaseExpression(ids, func(id int) string {
		return common.HashZTAPIKey(fmt.Sprintf("ztapi-invalid-legacy-token:%d", id))
	})
	result := db.Table("tokens").Where("id IN ?", ids).Updates(map[string]any{
		"status":     common.TokenStatusDisabled,
		"key":        gorm.Expr(keyExpression, keyArgs...),
		"key_hash":   gorm.Expr(hashExpression, hashArgs...),
		"key_prefix": "",
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(len(ids)) {
		return fmt.Errorf("invalidated %d of %d legacy tokens", result.RowsAffected, len(ids))
	}
	return nil
}

func shouldDropLegacyTokenKey() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("ZTAPI_DROP_LEGACY_TOKEN_KEY")))
	return err == nil && enabled
}

func migrateTokenKeyStorage() error {
	migrator := DB.Migrator()
	if !migrator.HasTable(&Token{}) {
		return nil
	}

	hasLegacyKey, err := tokenTableHasColumn("key")
	if err != nil {
		return fmt.Errorf("inspect legacy tokens.key: %w", err)
	}
	if !hasLegacyKey {
		return nil
	}

	hasKeyHash, err := tokenTableHasColumn("key_hash")
	if err != nil {
		return fmt.Errorf("inspect tokens.key_hash: %w", err)
	}
	if !hasKeyHash {
		if err := migrator.AddColumn(&Token{}, "KeyHash"); err != nil {
			return fmt.Errorf("add tokens.key_hash: %w", err)
		}
	}
	hasKeyPrefix, err := tokenTableHasColumn("key_prefix")
	if err != nil {
		return fmt.Errorf("inspect tokens.key_prefix: %w", err)
	}
	if !hasKeyPrefix {
		if err := migrator.AddColumn(&Token{}, "KeyPrefix"); err != nil {
			return fmt.Errorf("add tokens.key_prefix: %w", err)
		}
	}

	var legacyIDs []int
	if err := legacyTokenIDsQuery(DB).Order("id ASC").Pluck("id", &legacyIDs).Error; err != nil {
		return fmt.Errorf("list legacy plaintext tokens: %w", err)
	}
	const batchSize = 100
	for start := 0; start < len(legacyIDs); start += batchSize {
		end := min(start+batchSize, len(legacyIDs))
		if err := invalidateLegacyTokenBatch(DB, legacyIDs[start:end]); err != nil {
			return fmt.Errorf("invalidate legacy token batch starting at id %d: %w", legacyIDs[start], err)
		}
	}

	if !shouldDropLegacyTokenKey() {
		return nil
	}

	if err := migrator.DropColumn(&legacyTokenKeyColumn{}, "Key"); err != nil {
		return fmt.Errorf("drop legacy tokens.key: %w", err)
	}
	return nil
}
