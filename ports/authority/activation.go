// SPDX-License-Identifier: AGPL-3.0-only
package authority

// MemberActivationPermission is an owned allowlist of concrete keys enforced by
// current member domain operations. It deliberately excludes administration,
// account credential management, payments, permission management and wildcards.
// These are operator choices, not implicit defaults or recovered Shared values.
func MemberActivationPermission(key string) bool {
	switch key {
	case "tests.take", "posts.create", "posts.update", "posts.delete", "posts.react", "posts.bookmark", "chat.create", "chat.update", "chat.delete", "tickets.create", "tickets.update", "tickets.delete", "tickets.messages.create":
		return true
	default:
		return false
	}
}
