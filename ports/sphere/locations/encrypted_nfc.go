// SPDX-License-Identifier: AGPL-3.0-only
package locations

import "context"

type ProvisionTag struct {
	UID            string  `json:"uid"`
	SunKey         string  `json:"sun_key"`
	MetaKey        string  `json:"meta_key,omitempty"`
	AssignedUserID *string `json:"assigned_user_id"`
	MACProfile     string  `json:"mac_profile"`
}
type EncryptedTag struct {
	Tag
	AccountID *string `json:"user_id"`
}
type SUNRequest struct {
	RecordID  string `json:"record_id"`
	PICCData  string `json:"picc_data"`
	Encrypted string `json:"e,omitempty"`
	CMAC      string `json:"cmac"`
}
type EncryptedNFCRepository interface {
	ProvisionEncryptedTag(context.Context, string, ProvisionTag) (EncryptedTag, error)
	EncryptedTags(context.Context, string) ([]EncryptedTag, error)
	ResolveSUN(context.Context, string, SUNRequest, bool) (TagResolution, error)
	ChangeEncryptedTag(context.Context, string, string, TagPatch, bool) (Tag, error)
	LockEncryptedTag(context.Context, string, string) (Tag, error)
}

// NFCLocker must verify physical device authentication and apply durable write
// protection. An HTTP flag is never evidence that a physical tag was locked.
type NFCLockRequest struct {
	RecordID, UID, MACProfile string
	MetaKey, FileKey          []byte
	MinimumCounter            int
}
type NFCLockProof struct {
	SUN              SUNRequest
	TranscriptSHA256 string
}
type NFCLocker interface {
	Ready(context.Context) error
	Lock(context.Context, NFCLockRequest) (NFCLockProof, error)
}
