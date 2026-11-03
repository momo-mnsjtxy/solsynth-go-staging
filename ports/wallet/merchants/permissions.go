// SPDX-License-Identifier: AGPL-3.0-only
// Administrative wallet permissions are distinct from a customer's order scopes.
// HTTP and PostgreSQL guards use the same keys before exposing cross-account data.
package merchants

const (
	PermissionAdminOrdersView   = "admin.wallets.orders.view"
	PermissionAdminOrdersManage = "admin.wallets.orders.manage"
	PermissionAdminProductsView = "admin.wallets.products.view"
)
