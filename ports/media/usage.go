// SPDX-License-Identifier: AGPL-3.0-only
package media

// ValidateUsage recognizes the authored composer label only. Usage is descriptive
// metadata and never authorizes a read, selects a storage pool, or changes quota.
func ValidateUsage(value string) error {
	if value != "" && value != "post" {
		return ErrUnsupported
	}
	return nil
}

// UploadUsageSource lets a direct streaming parser provide fully validated
// trailing metadata after EOF. The canonical repository persists it in the same
// transaction as measured bytes and ready-state publication.
type UploadUsageSource interface{ UploadUsage() (string, error) }
