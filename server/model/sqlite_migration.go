package model

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type sqliteMigrationDialector struct{ gorm.Dialector }

func (d sqliteMigrationDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return sqliteMigration{Migrator: d.Dialector.Migrator(db), db: db}
}

type sqliteMigration struct {
	gorm.Migrator
	db *gorm.DB
}

type sqliteColumnMetadata interface{ gorm.ColumnType }

type sqliteDecimalColumn struct {
	sqliteColumnMetadata
	typ string
}

func (c sqliteDecimalColumn) DatabaseTypeName() string { return c.typ }
func (c sqliteDecimalColumn) Length() (int64, bool)    { return 0, false }

func (m sqliteMigration) ColumnTypes(value any) ([]gorm.ColumnType, error) {
	columns, err := m.Migrator.ColumnTypes(value)
	if err != nil {
		return nil, err
	}
	stmt := &gorm.Statement{DB: m.db}
	if err := stmt.Parse(value); err != nil {
		return nil, err
	}
	var decimals []int
	for i, column := range columns {
		if field := stmt.Schema.LookUpField(column.Name()); field != nil && strings.HasPrefix(string(field.DataType), "decimal(") {
			decimals = append(decimals, i)
		}
	}
	if len(decimals) == 0 {
		return columns, nil
	}
	var actual []struct{ Name, Type string }
	if err := m.db.Raw("SELECT name, type FROM pragma_table_info(?)", stmt.Table).Scan(&actual).Error; err != nil {
		return nil, err
	}
	types := make(map[string]string, len(actual))
	for _, info := range actual {
		types[info.Name] = info.Type
	}
	for _, i := range decimals {
		column := columns[i]
		declared := types[column.Name()]
		if strings.EqualFold(declared, string(stmt.Schema.LookUpField(column.Name()).DataType)) {
			// The SQLite driver truncates the type at the scale separator.
			columns[i] = sqliteDecimalColumn{column, declared}
		}
	}
	return columns, nil
}

func (m sqliteMigration) MigrateColumn(value any, field *schema.Field, column gorm.ColumnType) error {
	if field.GORMDataType == schema.Float && field.HasDefaultValue && field.DefaultValue == "0" &&
		strings.HasPrefix(string(field.DataType), "decimal(") && strings.EqualFold(column.DatabaseTypeName(), string(field.DataType)) {
		if raw, ok := column.DefaultValue(); ok && len(raw) <= 32 {
			if parsed, err := decimal.NewFromString(raw); err == nil && parsed.IsZero() {
				// GORM formats float defaults to six decimal places but compares them as strings.
				copy := *field
				copy.DefaultValue = raw
				field = &copy
			}
		}
	}
	if err := m.Migrator.MigrateColumn(value, field, column); err != nil {
		return fmt.Errorf("migrate %T.%s: %w", value, field.DBName, err)
	}
	return nil
}

func autoMigrateGatewaySchema(db *gorm.DB, models ...any) error {
	if db.Dialector.Name() == "sqlite" {
		db = db.Session(&gorm.Session{NewDB: true})
		db.Dialector = sqliteMigrationDialector{db.Dialector}
	}
	return db.AutoMigrate(models...)
}
