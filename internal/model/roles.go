package model

// Canonical RBAC roles (master CLAUDE.md). Only the subset this service gates
// on is declared here.
const (
	RolePracticeAdmin = "practice_admin"
	RoleReception     = "reception"

	RoleProviderSuperAdmin = "provider_super_admin"
	RoleProviderSupport    = "provider_support"
	RoleProviderBilling    = "provider_billing"
)
