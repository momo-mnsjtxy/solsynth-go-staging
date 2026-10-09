// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed DTOs with an explicitly owned canonical app/account association.
package bots

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"DEV_BOT_INVALID", "Invalid bot or miniapp request.", 400}
	ErrForbidden   = &Failure{"DEV_BOT_FORBIDDEN", "Current developer app authority is required.", 403}
	ErrNotFound    = &Failure{"DEV_BOT_ACCOUNT_NOT_FOUND", "Bot or associated app not found.", 404}
	ErrConflict    = &Failure{"DEV_BOT_CONFLICT", "The identity or slug is already reserved.", 409}
	ErrUnavailable = &Failure{"DEV_BOT_UNAVAILABLE", "The bot dependency is unavailable.", 503}
	ErrQuota       = &Failure{"DEV_BOT_QUOTA_EXCEEDED", "No current developer capacity is available.", 403}
	ErrUnsupported = &Failure{"DEV_BOT_PROVIDER_UNCONFIGURED", "The requested credential, media, or package provider is not configured.", 501}
)

type Profile struct {
	accounts.Profile
	FirstName  *string    `json:"first_name"`
	MiddleName *string    `json:"middle_name"`
	LastName   *string    `json:"last_name"`
	Gender     *string    `json:"gender"`
	Birthday   *time.Time `json:"birthday"`
}
type Account struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Nick        string    `json:"nick"`
	Language    string    `json:"language"`
	Region      string    `json:"region"`
	AutomatedID string    `json:"automated_id"`
	Profile     Profile   `json:"profile"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Developer struct {
	ID          string              `json:"id"`
	PublisherID string              `json:"publisher_id"`
	Name        string              `json:"name"`
	Publisher   *accounts.Publisher `json:"publisher"`
	CreatedAt   time.Time           `json:"created_at"`
}
type Bot struct {
	ID        string     `json:"id"`
	Slug      string     `json:"slug"`
	IsActive  bool       `json:"is_active"`
	ProjectID string     `json:"project_id"`
	AppID     string     `json:"app_id"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
	Account   Account    `json:"account"`
	Developer *Developer `json:"developer,omitempty"`
}
type Patch struct {
	Name         *string    `json:"name"`
	Nick         *string    `json:"nick"`
	Slug         *string    `json:"slug"`
	Language     *string    `json:"language"`
	IsActive     *bool      `json:"is_active"`
	FirstName    *string    `json:"first_name"`
	MiddleName   *string    `json:"middle_name"`
	LastName     *string    `json:"last_name"`
	Gender       *string    `json:"gender"`
	Pronouns     *string    `json:"pronouns"`
	TimeZone     *string    `json:"time_zone"`
	Location     *string    `json:"location"`
	Bio          *string    `json:"bio"`
	Birthday     *time.Time `json:"birthday"`
	PictureID    *string    `json:"picture_id"`
	BackgroundID *string    `json:"background_id"`
}
type ChatConfig struct {
	ID                   string               `json:"id"`
	Commands             []develop.BotCommand `json:"commands"`
	AutoApproveDM        bool                 `json:"auto_approve_dm"`
	AutoApproveGroupChat bool                 `json:"auto_approve_group_chat"`
	SupportChat          bool                 `json:"support_chat"`
	SubscribedEvents     []string             `json:"subscribed_events"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
}
type ManifestPatch struct {
	Commands             *[]develop.BotCommand `json:"commands"`
	AutoApproveDM        *bool                 `json:"auto_approve_dm"`
	AutoApproveGroupChat *bool                 `json:"auto_approve_group_chat"`
	SupportChat          *bool                 `json:"support_chat"`
	SubscribedEvents     *[]string             `json:"subscribed_events"`
}
type Key struct {
	ID        string     `json:"id"`
	Secret    *string    `json:"secret,omitempty"`
	Label     string     `json:"label"`
	BotID     string     `json:"bot_id"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}
type KeyRequest struct {
	Label string `json:"label"`
}
type ReplyRequest struct {
	RoomID           string                `json:"room_id"`
	Content          string                `json:"content"`
	EncryptionMeta   *rooms.EncryptionMeta `json:"encryption_meta,omitempty"`
	RepliedMessageID *string               `json:"replied_message_id,omitempty"`
	ClientMessageID  *string               `json:"client_message_id,omitempty"`
	DeviceID         string                `json:"device_id,omitempty"`
	MLSStateVersion  *int64                `json:"mls_state_version,omitempty"`
}
type MiniManifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Author      *string  `json:"author"`
	Description *string  `json:"description"`
	Entry       string   `json:"entry"`
	Permissions []string `json:"permissions"`
	Background  bool     `json:"background"`
	Icon        *string  `json:"icon"`
	Homepage    *string  `json:"homepage"`
	EntryURL    *string  `json:"entry_url"`
}
type MiniApp struct {
	ID            string  `json:"id"`
	Slug          string  `json:"slug"`
	PluginID      *string `json:"plugin_id"`
	Name          *string `json:"name"`
	Version       string  `json:"version"`
	Author        *string `json:"author"`
	Description   *string `json:"description"`
	EntryURL      *string `json:"entry_url"`
	Icon          any     `json:"icon"`
	Background    any     `json:"background"`
	Homepage      *string `json:"homepage"`
	PackageURL    *string `json:"package_url"`
	PackageSHA256 *string `json:"package_sha256"`
	PackageSize   *int64  `json:"package_size"`
	DownloadCount int64   `json:"download_count"`
	// DownloadCount counts authorized package opens, including conditional,
	// ranged, and interrupted transfers after canonical storage access succeeds.
	DownloadCountSemantics string        `json:"download_count_semantics"`
	Stage                  int           `json:"stage"`
	Manifest               *MiniManifest `json:"manifest"`
	ProjectID              string        `json:"project_id"`
	AppID                  string        `json:"app_id"`
	Developer              *Developer    `json:"developer,omitempty"`
	CreatedAt              time.Time     `json:"created_at"`
	UpdatedAt              time.Time     `json:"updated_at"`
	DeletedAt              *time.Time    `json:"deleted_at"`
}
type MiniPatch struct {
	Slug         *string       `json:"slug"`
	Stage        *int          `json:"stage"`
	Manifest     *MiniManifest `json:"manifest"`
	IconID       *string       `json:"icon_id"`
	BackgroundID *string       `json:"background_id"`
}
type MediaProvider interface {
	ResolvePublicInTransaction(context.Context, *sql.Tx, string, string) (media.File, error)
	Open(context.Context, string, string) (media.File, io.ReadSeekCloser, error)
}
type PackageResult struct {
	Key         string `json:"key"`
	URL         string `json:"url"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}
type Repository interface {
	Keys(context.Context, apps.Scope, string, string, *KeyRequest, string) ([]Key, error)
	Reply(context.Context, string, string, ReplyRequest) (messager.MessageProjection, error)
	LinkPackage(context.Context, apps.Scope, string, string) (PackageResult, error)
	OpenPackage(context.Context, string) (media.File, io.ReadSeekCloser, error)
	ListBots(context.Context, apps.Scope, int, int) ([]Bot, int64, error)
	ListMiniApps(context.Context, apps.Scope, int, int) ([]MiniApp, int64, error)
	Bots(context.Context, apps.Scope, string, *Patch, string) ([]Bot, error)
	PublicBot(context.Context, string) (Bot, error)
	PublicDeveloper(context.Context, string) (Developer, error)
	Chat(context.Context, apps.Scope, string, *ManifestPatch, bool) (ChatConfig, error)
	PublicChat(context.Context, string) (ChatConfig, error)
	Command(context.Context, apps.Scope, string, string, *develop.BotCommand, string) ([]develop.BotCommand, error)
	MiniApps(context.Context, apps.Scope, string, *MiniPatch, string) ([]MiniApp, error)
	DiscoverMiniApps(context.Context, int, int, string) ([]MiniApp, int64, error)
	PublicMiniApp(context.Context, string) (MiniApp, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}

func DefaultChat(id string, at time.Time) ChatConfig {
	return ChatConfig{ID: id, Commands: []develop.BotCommand{}, AutoApproveDM: true, SupportChat: true, SubscribedEvents: []string{"messages.new"}, CreatedAt: at, UpdatedAt: at}
}
func CloneCommands(v []develop.BotCommand) []develop.BotCommand {
	if v == nil {
		return []develop.BotCommand{}
	}
	b, _ := json.Marshal(v)
	var out []develop.BotCommand
	_ = json.Unmarshal(b, &out)
	return out
}
func (m *MiniManifest) UnmarshalJSON(raw []byte) error {
	type plain MiniManifest
	v := plain{Version: "1.0.0", Entry: "main.js", Permissions: []string{}}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&v); e != nil {
		return e
	}
	*m = MiniManifest(v)
	return nil
}
