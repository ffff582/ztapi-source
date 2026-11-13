package model

import "errors"

// Common errors
var (
	ErrDatabase = errors.New("database error")
)

// User auth errors
var (
	ErrInvalidCredentials    = errors.New("invalid credentials")
	ErrUserEmptyCredentials  = errors.New("empty credentials")
	ErrInsufficientUserQuota = errors.New("insufficient user quota")
)

// Token auth errors
var (
	ErrTokenNotProvided       = errors.New("token not provided")
	ErrTokenInvalid           = errors.New("token invalid")
	ErrInsufficientTokenQuota = errors.New("insufficient token quota")
)

// Redemption errors
var ErrRedeemFailed = errors.New("redeem.failed")

// 2FA errors
var ErrTwoFANotEnabled = errors.New("2fa not enabled")
