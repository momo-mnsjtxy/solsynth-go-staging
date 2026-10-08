// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"context"
	"io"
	"time"
)

const DefaultChunkBytes int64 = 5 << 20
const MaxChunkBytes int64 = 20 << 20
const MaxUploadChunks = 10000

// CreateUploadRequest preserves the authored Drive field names. Unsupported
// provider, hierarchy and encryption options are rejected rather than ignored.
type CreateUploadRequest struct {
	Hash                string     `json:"hash"`
	FileName            string     `json:"file_name"`
	FileSize            int64      `json:"file_size"`
	ContentType         string     `json:"content_type"`
	ChunkSize           int64      `json:"chunk_size"`
	PoolID              string     `json:"pool_id"`
	BundleID            string     `json:"bundle_id"`
	ParentID            string     `json:"parent_id"`
	EncryptionScheme    string     `json:"encryption_scheme"`
	EncryptionHeader    string     `json:"encryption_header"`
	EncryptionSignature string     `json:"encryption_signature"`
	ExpiredAt           *time.Time `json:"expired_at"`
	Usage               string     `json:"usage"`
	ApplicationType     string     `json:"application_type"`
}

type UploadTask struct {
	TaskID         string    `json:"task_id"`
	FileName       string    `json:"file_name"`
	FileSize       int64     `json:"file_size"`
	ContentType    string    `json:"content_type"`
	ChunkSize      int64     `json:"chunk_size"`
	ChunksCount    int       `json:"chunks_count"`
	ChunksUploaded int       `json:"chunks_uploaded"`
	UploadedChunks []int     `json:"uploaded_chunks"`
	Progress       float64   `json:"progress"`
	Status         string    `json:"status"`
	LastActivity   time.Time `json:"last_activity"`
	FileID         *string   `json:"file_id,omitempty"`
	FailureCode    string    `json:"failure_code,omitempty"`
}

type UploadTaskQuery struct {
	Status, SortBy string
	Descending     bool
	Offset, Limit  int
}

type ResumableRepository interface {
	CreateResumable(context.Context, string, CreateUploadRequest, string) (UploadTask, error)
	UploadChunk(context.Context, string, string, int, io.Reader) (bool, error)
	CompleteResumable(context.Context, string, string) error
	ResumableProgress(context.Context, string, string) (UploadTask, error)
	ListResumable(context.Context, string, UploadTaskQuery) ([]UploadTask, error)
	CancelResumable(context.Context, string, string) error
}

// Chunk objects are private staging bytes. Repository transactions serialize all
// operations on a task; stores never interpret a caller's filename as a path.
type StoredChunk struct {
	Index  int
	Size   int64
	SHA256 string
}
type ChunkObjectStore interface {
	PutChunk(context.Context, string, int, io.Reader, int64, string) (string, error)
	AssembleChunks(context.Context, string, []StoredChunk, int64, string) (string, error)
}
