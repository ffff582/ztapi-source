package common

// AdminPermission is a named capability for ZTAPI management endpoints.
type AdminPermission string

const (
	PermissionOverviewRead    AdminPermission = "overview.read"
	PermissionChannelRead     AdminPermission = "channel.read"
	PermissionChannelWrite    AdminPermission = "channel.write"
	PermissionModelRead       AdminPermission = "model.read"
	PermissionModelWrite      AdminPermission = "model.write"
	PermissionUserRead        AdminPermission = "user.read"
	PermissionUserStatusWrite AdminPermission = "user.status.write"
	PermissionBalanceWrite    AdminPermission = "balance.write"
	PermissionFinanceRead     AdminPermission = "finance.read"
	PermissionFinanceWrite    AdminPermission = "finance.write"
	PermissionLogRead         AdminPermission = "log.read"
	PermissionSystemWrite     AdminPermission = "system.write"
	PermissionRoleWrite       AdminPermission = "role.write"
	PermissionAuditRead       AdminPermission = "audit.read"
)

// HasAdminPermission is deliberately an explicit policy table. Role values are
// identifiers, not an authorization hierarchy, so unknown values fail closed.
func HasAdminPermission(role int, permission AdminPermission) bool {
	switch role {
	case RoleSupportUser:
		switch permission {
		case PermissionOverviewRead, PermissionChannelRead, PermissionModelRead, PermissionUserRead, PermissionUserStatusWrite, PermissionLogRead:
			return true
		default:
			return false
		}
	case RoleFinanceUser:
		switch permission {
		case PermissionOverviewRead, PermissionFinanceRead, PermissionFinanceWrite:
			return true
		default:
			return false
		}
	case RoleAdminUser:
		switch permission {
		case PermissionOverviewRead, PermissionChannelRead, PermissionChannelWrite, PermissionModelRead, PermissionModelWrite, PermissionUserRead, PermissionUserStatusWrite, PermissionBalanceWrite, PermissionFinanceRead, PermissionFinanceWrite, PermissionLogRead, PermissionAuditRead:
			return true
		default:
			return false
		}
	case RoleRootUser:
		switch permission {
		case PermissionOverviewRead, PermissionChannelRead, PermissionChannelWrite, PermissionModelRead, PermissionModelWrite, PermissionUserRead, PermissionUserStatusWrite, PermissionBalanceWrite, PermissionFinanceRead, PermissionFinanceWrite, PermissionLogRead, PermissionSystemWrite, PermissionRoleWrite, PermissionAuditRead:
			return true
		default:
			return false
		}
	default:
		return false
	}
}
