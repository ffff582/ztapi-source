// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const customerServicePurpose = "customer_service"

// The existing immutable fact kind is retained so preceding readers can still
// decode the ledger. Missing purpose keeps its historical management-test meaning.
func validateInternalCostPurpose(c *TestCost) error {
	if c.Purpose != "" && c.Purpose != "management_test" && c.Purpose != customerServicePurpose {
		return ErrInvalid
	}
	if c.InputTokenScope != "" && c.InputTokenScope != "inclusive" && c.InputTokenScope != "uncached" {
		return ErrInvalid
	}
	hasCache := false
	remaining := c.InputTokens
	var included int64
	if remaining != nil {
		included = *remaining
	}
	for _, tokens := range []*int64{c.CacheReadTokens, c.CacheWrite5mTokens, c.CacheWrite1hTokens} {
		if tokens == nil {
			continue
		}
		hasCache = true
		if *tokens < 0 {
			return ErrInvalid
		}
		if c.InputTokenScope == "inclusive" && remaining != nil {
			if *tokens > included {
				return ErrInvalid
			}
			included -= *tokens
		}
	}
	if hasCache && c.InputTokenScope == "" {
		return ErrInvalid
	}
	return nil
}

func internalCostIssue(c TestCost, code string) string {
	if c.Purpose == customerServicePurpose {
		return strings.Replace(code, "test_cost_", "customer_service_cost_", 1)
	}
	return code
}

func customerServiceOwner(s Snapshot, userID int) bool {
	for _, user := range s.Users {
		if user.ID == userID && user.DeletedAt == nil &&
			(user.Role == common.RoleAdminUser || user.Role == common.RoleRootUser) {
			return true
		}
	}
	return false
}
