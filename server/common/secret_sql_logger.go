package common

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SecretSQLLogger is both a GORM logger and a plugin. Install it with db.Use
// to protect Scan, which temporarily replaces the logger in GORM v1.25.
type SecretSQLLogger struct {
	gormlogger.Interface
}

func NewSecretSQLLogger(base gormlogger.Interface) *SecretSQLLogger {
	return &SecretSQLLogger{Interface: base}
}

type secretSQLTraceKey struct{}

type secretSQLTrace struct {
	template    string
	boundValues []string
}

func (l *SecretSQLLogger) Name() string { return "secret_sql_logger" }

func (l *SecretSQLLogger) Initialize(db *gorm.DB) error {
	capture := func(tx *gorm.DB) {
		trace := secretSQLTrace{template: RedactSecrets(tx.Statement.SQL.String())}
		// Scan can fail after this callback and after GORM clears Statement.Vars.
		for _, param := range tx.Statement.Vars {
			var value string
			switch v := param.(type) {
			case string:
				value = v
			case *string:
				if v != nil {
					value = *v
				}
			case []byte:
				value = string(v)
			case *[]byte:
				if v != nil {
					value = string(*v)
				}
			}
			if value != "" {
				trace.boundValues = append(trace.boundValues, value)
			}
		}
		tx.Statement.Context = context.WithValue(tx.Statement.Context, secretSQLTraceKey{}, trace)
	}
	for _, register := range []func(string, func(*gorm.DB)) error{
		db.Callback().Create().After("*").Register,
		db.Callback().Query().After("*").Register,
		db.Callback().Update().After("*").Register,
		db.Callback().Delete().After("*").Register,
		db.Callback().Row().After("*").Register,
		db.Callback().Raw().After("*").Register,
	} {
		if err := register("secret_sql:template", capture); err != nil {
			return err
		}
	}
	return nil
}

func (l *SecretSQLLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return NewSecretSQLLogger(l.Interface.LogMode(level))
}

func (l *SecretSQLLogger) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return RedactSecrets(sql), nil
}

func (l *SecretSQLLogger) Info(ctx context.Context, msg string, args ...interface{}) {
	l.Interface.Info(ctx, "%s", RedactSecrets(fmt.Sprintf(msg, args...)))
}

func (l *SecretSQLLogger) Warn(ctx context.Context, msg string, args ...interface{}) {
	l.Interface.Warn(ctx, "%s", RedactSecrets(fmt.Sprintf(msg, args...)))
}

func (l *SecretSQLLogger) Error(ctx context.Context, msg string, args ...interface{}) {
	l.Interface.Error(ctx, "%s", RedactSecrets(fmt.Sprintf(msg, args...)))
}

type secretSQLLogError struct {
	cause   error
	message string
}

func (e secretSQLLogError) Error() string { return e.message }
func (e secretSQLLogError) Unwrap() error { return e.cause }

func (l *SecretSQLLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	trace, hasTemplate := ctx.Value(secretSQLTraceKey{}).(secretSQLTrace)
	if err != nil {
		message := err.Error()
		for _, value := range trace.boundValues {
			message = strings.ReplaceAll(message, strconv.Quote(value), `"[REDACTED]"`)
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
		err = secretSQLLogError{cause: err, message: RedactSecrets(message)}
	}
	l.Interface.Trace(ctx, begin, func() (string, int64) {
		sql, rows := fc()
		if hasTemplate {
			sql = trace.template
		}
		return RedactSecrets(sql), rows
	}, err)
}
