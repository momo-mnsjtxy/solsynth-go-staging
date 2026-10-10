// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/rtcprovider"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/federation"
	"strings"
	"syscall"
	"time"
)

type externalFlagSet struct {
	outputs                                      outputSettings
	streamChat                                   *bool
	streamMessages, streamEvents, streamAttempts *int
	streamEventTTL, streamSendTimeout            *time.Duration
	streams                                      *bool
	streamTTL, streamLifetime, streamIdle        *time.Duration
	streamLimit                                  *int
	calls, federation, send, cloudRevocation     *bool
	discover                                     *bool
	admit                                        *bool
	endpoint, keyEnv, secretEnv, federationFile  *string
	tokenTTL, callTTL, maintenance               *time.Duration
}

func externalFlags(fs *flag.FlagSet) externalFlagSet {
	return externalFlagSet{
		outputs:           livestreamOutputFlags(fs),
		streamChat:        fs.Bool("enable-livestream-chat", false, "enable durable canonical livestream chat and configured provider data delivery"),
		streamMessages:    fs.Int("livestream-chat-message-limit", 100000, "retained messages per stream including deleted messages"),
		streamEvents:      fs.Int("livestream-chat-event-limit", 200000, "retained durable chat events per stream"),
		streamAttempts:    fs.Int("livestream-chat-attempts", 3, "bounded provider acceptance attempts"),
		streamEventTTL:    fs.Duration("livestream-chat-event-ttl", 5*time.Minute, "maximum queued event lifetime"),
		streamSendTimeout: fs.Duration("livestream-chat-send-timeout", 3*time.Second, "bounded provider data request timeout"),
		streams:           fs.Bool("enable-livestreams", false, "enable canonical livestreams through explicitly configured LiveKit"),
		streamTTL:         fs.Duration("livestream-token-ttl", time.Minute, "short-lived viewer or publisher token duration"),
		streamLifetime:    fs.Duration("livestream-max-lifetime", 4*time.Hour, "maximum broadcast lifetime"),
		streamIdle:        fs.Duration("livestream-idle-grace", 5*time.Minute, "idle ingress grace, 5m through 1h"),
		streamLimit:       fs.Int("livestream-publisher-limit", 20, "maximum retained streams per publisher"),
		calls:             fs.Bool("enable-calls", false, "enable canonical room-call signaling with explicitly provisioned LiveKit Cloud credentials"),
		federation:        fs.Bool("enable-federation", false, "enable the explicitly provisioned ActivityPub subset"),
		send:              fs.Bool("federation-send", false, "enable queued outbound delivery only to configured pinned HTTPS peers"),
		admit:             fs.Bool("federation-admit", false, "enable bounded first-contact admission for explicitly initialized public-origin policies"),
		discover:          fs.Bool("federation-discover", false, "enable bounded WebFinger/actor observations for explicitly configured pinned targets"),
		cloudRevocation:   fs.Bool("livekit-cloud-revocation", false, "assert this deployment supports Cloud revoke_token_ts; self-hosted removal is insufficient"),
		endpoint:          fs.String("livekit-endpoint", "", "explicit WSS LiveKit endpoint"), keyEnv: fs.String("livekit-api-key-env", "", "explicit environment variable holding the LiveKit API key"), secretEnv: fs.String("livekit-api-secret-env", "", "explicit environment variable holding the raw LiveKit API secret"),
		federationFile: fs.String("federation-config", "", "absolute path to a JSON origin/peer/key-reference configuration; mutation requires explicit local custody"),
		tokenTTL:       fs.Duration("call-token-ttl", 2*time.Minute, "short-lived participant token duration, 15s through 5m"), callTTL: fs.Duration("call-max-lifetime", 4*time.Hour, "maximum call lifetime, at most 24h"), maintenance: fs.Duration("external-maintenance-interval", 5*time.Second, "configured call/federation maintenance cadence, 1s through 30s"),
	}
}
func (v externalFlagSet) apply(c *application.Config) error {
	outputPolicy, outputConfig, archiveImport, outputError := v.outputs.load(*v.streams, c)
	if outputError != nil {
		return outputError
	}
	if *v.streamChat && !*v.streams {
		return errors.New("livestream chat requires explicit livestream enablement")
	}
	if !*v.streamChat && (*v.streamMessages != 100000 || *v.streamEvents != 200000 || *v.streamAttempts != 3 || *v.streamEventTTL != 5*time.Minute || *v.streamSendTimeout != 3*time.Second) {
		return errors.New("chat limits require explicit chat enablement")
	}
	if *v.streamChat && (*v.streamMessages < 1 || *v.streamMessages > 100000 || *v.streamEvents < *v.streamMessages || *v.streamEvents > 200000 || *v.streamAttempts < 1 || *v.streamAttempts > 20 || *v.streamEventTTL < time.Minute || *v.streamEventTTL > 24*time.Hour || *v.streamSendTimeout <= 0 || *v.streamSendTimeout > 5*time.Second) {
		return errors.New("invalid bounded livestream chat policy")
	}
	if *v.maintenance < time.Second || *v.maintenance > 30*time.Second {
		return errors.New("external maintenance interval must be between 1 and 30 seconds")
	}
	if !*v.streams && (*v.streamTTL != time.Minute || *v.streamLifetime != 4*time.Hour || *v.streamIdle != 5*time.Minute || *v.streamLimit != 20) {
		return errors.New("livestream limits require explicit enablement")
	}
	if *v.streams && (*v.streamTTL < 15*time.Second || *v.streamTTL > 5*time.Minute || *v.streamLifetime < *v.streamTTL || *v.streamLifetime > 24*time.Hour || *v.streamIdle < 5*time.Minute || *v.streamIdle > time.Hour || *v.streamLimit < 1 || *v.streamLimit > 10000) {
		return errors.New("invalid livestream policy")
	}
	if !*v.calls && !*v.streams {
		if *v.endpoint != "" || *v.keyEnv != "" || *v.secretEnv != "" || *v.cloudRevocation {
			return errors.New("LiveKit provider settings require enable-calls or enable-livestreams")
		}
	} else {
		if *v.keyEnv == "" || *v.secretEnv == "" || *v.tokenTTL < 15*time.Second || *v.tokenTTL > 5*time.Minute || *v.callTTL < *v.tokenTTL || *v.callTTL > 24*time.Hour {
			return errors.New("invalid explicit call provider policy")
		}
		secret := []byte(os.Getenv(*v.secretEnv))
		defer clear(secret)
		transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32768, MaxIdleConns: 20, IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: true}
		providerConfig := rtcprovider.Config{Endpoint: *v.endpoint, APIKey: os.Getenv(*v.keyEnv), APISecret: secret, CloudRevocation: *v.cloudRevocation, Timeout: 5 * time.Second}
		var provider *rtcprovider.LiveKit
		var e error
		if outputPolicy != nil {
			provider, e = rtcprovider.NewWithOutputs(providerConfig, *outputPolicy, transport)
		} else {
			provider, e = rtcprovider.New(providerConfig, transport)
		}
		if e != nil {
			return errors.New("LiveKit provider configuration is incomplete or unsupported")
		}
		if *v.calls {
			c.Calls = &application.CallConfig{Provider: provider, TokenTTL: *v.tokenTTL, CallTTL: *v.callTTL}
		}
		if *v.streams {
			c.Livestreams = &application.LivestreamConfig{Provider: provider, Outputs: outputConfig, ArchiveImport: archiveImport, Policy: livestreampg.Config{TokenTTL: *v.streamTTL, MaxRunDuration: *v.streamLifetime, IdleGrace: *v.streamIdle, MaxStreamsPerPublisher: *v.streamLimit}}
		}
	}
	if *v.streamChat {
		c.Livestreams.Chat = &livestreampg.ChatConfig{MaxMessagesPerStream: *v.streamMessages, MaxEventsPerStream: *v.streamEvents, MaxAttempts: *v.streamAttempts, EventTTL: *v.streamEventTTL, SendTimeout: *v.streamSendTimeout}
	}
	if !*v.federation {
		if *v.federationFile != "" || *v.send || *v.discover || *v.admit {
			return errors.New("federation settings require enable-federation")
		}
		return nil
	}
	cfg, e := loadFederationConfigWithAdmission(*v.federationFile, *v.send, *v.discover, *v.admit)
	if e != nil {
		return e
	}
	c.Federation = cfg
	return nil
}

type federationFile struct {
	PublicAdmission           []federation.PublicAdmissionPolicy `json:"public_admission"`
	RetiredRemoteActors       []string                           `json:"retired_remote_actors"`
	RetiredRemoteKeyIDs       []string                           `json:"retired_remote_key_ids"`
	RetiredRemoteFingerprints []string                           `json:"retired_remote_key_fingerprints"`
	ServerActor               *struct {
		Username      string `json:"username"`
		Name          string `json:"name"`
		Summary       string `json:"summary"`
		PublicKeyFile string `json:"public_key_file"`
	} `json:"server_actor"`
	DiscoveryTargets       []federation.DiscoveryTarget `json:"discovery_targets"`
	Origin                 string                       `json:"origin"`
	AllowedOrigins         []string                     `json:"allowed_origins"`
	MaxAttempts            int                          `json:"max_attempts"`
	DeliveryTimeoutSeconds int                          `json:"delivery_timeout_seconds"`
	LocalKeyCustody        *struct {
		KeyDirectory       string `json:"key_directory"`
		OperationDirectory string `json:"operation_directory"`
	} `json:"local_key_custody"`
	LocalKeys []struct {
		PublisherID    string `json:"publisher_id"`
		PrivateKeyFile string `json:"private_key_file"`
	} `json:"local_keys"`
	RemoteKeys []struct {
		Actor         string `json:"actor"`
		Inbox         string `json:"inbox"`
		KeyID         string `json:"key_id"`
		Incarnation   string `json:"incarnation"`
		PublicKeyFile string `json:"public_key_file"`
	} `json:"remote_keys"`
}
type configuredFederationKeys struct {
	retiredActors, retiredKeys map[string]bool
	retiredFingerprints        map[string]bool
	local                      map[string]crypto.Signer
	localFiles                 map[string]string
	custody                    *fileFederationCustody
	remote                     map[string]federation.RemoteKey
	actors                     map[string]federation.RemoteKey
}
type configuredFederationCustodyKeys struct{ configuredFederationKeys }

func (k configuredFederationKeys) LocalKey(ctx context.Context, id string) (crypto.Signer, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if path := k.localFiles[id]; path != "" {
		if securePrivateKeyFile(path) != nil {
			return nil, federation.ErrUnavailable
		}
		s, e := issuerKey(path)
		if e != nil {
			return nil, federation.ErrUnavailable
		}
		return s, nil
	}
	s, ok := k.local[id]
	if !ok {
		return nil, federation.ErrUnavailable
	}
	return s, nil
}
func (k configuredFederationCustodyKeys) LocalKey(ctx context.Context, id string) (crypto.Signer, error) {
	if k.custody == nil {
		return nil, federation.ErrUnavailable
	}
	path := k.localFiles[id]
	if path == "" {
		return nil, federation.ErrNotFound
	}
	return k.custody.LocalKey(ctx, id, path, k.localFiles)
}
func (k configuredFederationCustodyKeys) ExecuteLocalKeyCommand(ctx context.Context, operation, id, action, expected string) (string, error) {
	if k.custody == nil {
		return "", federation.ErrUnavailable
	}
	path := k.localFiles[id]
	if path == "" {
		return "", federation.ErrNotFound
	}
	return k.custody.Execute(ctx, operation, id, path, action, expected, k.localFiles)
}
func (k configuredFederationKeys) RemoteKey(ctx context.Context, id string) (federation.RemoteKey, error) {
	if e := ctx.Err(); e != nil {
		return federation.RemoteKey{}, e
	}
	s, ok := k.remote[id]
	if !ok {
		return s, federation.ErrUnavailable
	}
	return s, nil
}
func (k configuredFederationKeys) RemoteActor(ctx context.Context, id string) (federation.RemoteKey, error) {
	if e := ctx.Err(); e != nil {
		return federation.RemoteKey{}, e
	}
	s, ok := k.actors[id]
	if !ok {
		return s, federation.ErrUnavailable
	}
	return s, nil
}
func loadFederationConfig(name string, send bool) (*application.FederationConfig, error) {
	return loadFederationConfigWithDiscovery(name, send, false)
}
func loadFederationConfigWithDiscovery(name string, send, discover bool) (*application.FederationConfig, error) {
	return loadFederationConfigWithAdmission(name, send, discover, false)
}
func loadFederationConfigWithAdmission(name string, send, discover, admit bool) (*application.FederationConfig, error) {
	invalid := errors.New("invalid explicit federation configuration")
	if !filepath.IsAbs(name) {
		return nil, invalid
	}
	file, e := os.Open(name)
	if e != nil {
		return nil, invalid
	}
	defer file.Close()
	raw, e := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if e != nil || len(raw) > 1<<20 || federation.CheckJSON(raw) != nil || !canonicalFederationFields(raw) {
		return nil, invalid
	}
	var input federationFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		return nil, invalid
	}
	policy := federation.Config{Origin: input.Origin, AllowedOrigins: input.AllowedOrigins, MaxAttempts: input.MaxAttempts, DeliveryTimeout: time.Duration(input.DeliveryTimeoutSeconds) * time.Second}
	if policy.Validate() != nil || len(input.AllowedOrigins) > 128 || len(input.LocalKeys) < 1 && input.ServerActor == nil && len(input.PublicAdmission) == 0 || len(input.LocalKeys) > 1000 || len(input.RemoteKeys) > 1000 || len(input.DiscoveryTargets) > 128 || len(input.RetiredRemoteActors) > 1000 || len(input.RetiredRemoteKeyIDs) > 1000 || len(input.RetiredRemoteFingerprints) > 1000 {
		return nil, invalid
	}
	keys := configuredFederationKeys{local: map[string]crypto.Signer{}, localFiles: map[string]string{}, remote: map[string]federation.RemoteKey{}, actors: map[string]federation.RemoteKey{}}
	if input.LocalKeyCustody != nil {
		keys.custody, e = newFileFederationCustody(input.LocalKeyCustody.KeyDirectory, input.LocalKeyCustody.OperationDirectory)
		if e != nil {
			return nil, invalid
		}
	}
	localPaths := map[string]bool{}
	localInodes := map[[2]uint64]bool{}
	localFingerprints := map[string]bool{}
	for _, local := range input.LocalKeys {
		if !authority.ValidID(local.PublisherID) || !filepath.IsAbs(local.PrivateKeyFile) || keys.local[local.PublisherID] != nil || keys.localFiles[local.PublisherID] != "" {
			return nil, invalid
		}
		clean := filepath.Clean(local.PrivateKeyFile)
		if localPaths[clean] {
			return nil, invalid
		}
		localPaths[clean] = true
		info, statErr := os.Lstat(clean)
		missing := errors.Is(statErr, os.ErrNotExist)
		if statErr != nil && !(missing && keys.custody != nil) || statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0) {
			return nil, invalid
		}
		if statErr == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return nil, invalid
			}
			inode := [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}
			if localInodes[inode] {
				return nil, invalid
			}
			localInodes[inode] = true
		}
		if keys.custody != nil {
			if filepath.Dir(clean) != keys.custody.keyDirectory {
				return nil, invalid
			}
			if !missing {
				var signer crypto.Signer
				if signer, e = issuerKey(clean); e != nil {
					return nil, invalid
				}
				public, ok := signer.Public().(*rsa.PublicKey)
				if !ok {
					return nil, invalid
				}
				fingerprint, fingerprintErr := federation.KeyFingerprint(public)
				if fingerprintErr != nil || localFingerprints[fingerprint] {
					return nil, invalid
				}
				localFingerprints[fingerprint] = true
			}
			keys.localFiles[local.PublisherID] = clean
		} else {
			signer, e := issuerKey(clean)
			if e != nil {
				return nil, invalid
			}
			public, ok := signer.Public().(*rsa.PublicKey)
			if !ok {
				return nil, invalid
			}
			fingerprint, fingerprintErr := federation.KeyFingerprint(public)
			if fingerprintErr != nil || localFingerprints[fingerprint] {
				return nil, invalid
			}
			localFingerprints[fingerprint] = true
			keys.local[local.PublisherID] = signer
		}
	}
	for _, remote := range input.RemoteKeys {
		if !filepath.IsAbs(remote.PublicKeyFile) {
			return nil, invalid
		}
		key, e := publicKey(remote.PublicKeyFile)
		if e != nil {
			return nil, invalid
		}
		bound := federation.RemoteKey{Actor: remote.Actor, Inbox: remote.Inbox, KeyID: remote.KeyID, Incarnation: remote.Incarnation, PublicKey: key}
		if !authority.ValidID(bound.Incarnation) || !policy.ValidRemote(bound) || keys.remote[bound.KeyID].KeyID != "" || keys.actors[bound.Actor].Actor != "" {
			return nil, invalid
		}
		keys.remote[bound.KeyID] = bound
		keys.actors[bound.Actor] = bound
	}
	keys.retiredActors = map[string]bool{}
	keys.retiredKeys = map[string]bool{}
	keys.retiredFingerprints = map[string]bool{}
	for _, a := range input.RetiredRemoteActors {
		if !policy.Allows(a) || keys.retiredActors[a] || keys.actors[a].Actor != "" {
			return nil, invalid
		}
		keys.retiredActors[a] = true
	}
	for _, id := range input.RetiredRemoteKeyIDs {
		u, e := federation.ParseURL(id)
		if e != nil || u.Fragment == "" || keys.retiredKeys[id] || keys.remote[id].KeyID != "" {
			return nil, invalid
		}
		keys.retiredKeys[id] = true
	}
	for _, fingerprint := range input.RetiredRemoteFingerprints {
		decoded, e := hex.DecodeString(fingerprint)
		if e != nil || len(decoded) != 32 || strings.ToLower(fingerprint) != fingerprint || keys.retiredFingerprints[fingerprint] {
			return nil, invalid
		}
		for _, pin := range keys.remote {
			current, e := federation.KeyFingerprint(pin.PublicKey)
			if e != nil || current == fingerprint {
				return nil, invalid
			}
		}
		keys.retiredFingerprints[fingerprint] = true
	}
	var keyProvider federation.KeyProvider = keys
	if keys.custody != nil {
		keyProvider = configuredFederationCustodyKeys{keys}
	}
	cfg := &application.FederationConfig{Policy: policy, Keys: keyProvider}
	if len(input.PublicAdmission) > 128 {
		return nil, invalid
	}
	seenOrigins := map[string]bool{}
	for _, p := range input.PublicAdmission {
		if p.Validate(policy) != nil || seenOrigins[p.Origin] {
			return nil, invalid
		}
		seenOrigins[p.Origin] = true
		cfg.PublicAdmission = append(cfg.PublicAdmission, p)
	}
	if admit {
		cfg.AdmissionFetcher, e = federation.NewPublicAdmissionFetcher(policy, cfg.PublicAdmission)
		if e != nil {
			return nil, invalid
		}
	}
	if input.ServerActor != nil {
		if !filepath.IsAbs(input.ServerActor.PublicKeyFile) {
			return nil, invalid
		}
		key, e := publicKey(input.ServerActor.PublicKeyFile)
		if e != nil {
			return nil, invalid
		}
		der, e := x509.MarshalPKIXPublicKey(key)
		if e != nil {
			return nil, invalid
		}
		server := federation.ServerConfig{Username: input.ServerActor.Username, Name: input.ServerActor.Name, Summary: input.ServerActor.Summary, PublicKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
		if _, e = server.Actor(policy); e != nil {
			return nil, invalid
		}
		cfg.Discovery.Server = &server
	}
	seenTargets := map[string]bool{}
	for _, target := range input.DiscoveryTargets {
		if target.Validate(policy) != nil || seenTargets[target.Resource] {
			return nil, invalid
		}
		if _, e := target.Binding(policy, keys.actors[target.Actor]); e != nil {
			return nil, invalid
		}
		seenTargets[target.Resource] = true
		cfg.Discovery.Targets = append(cfg.Discovery.Targets, target)
	}
	if discover {
		cfg.Fetcher, e = federation.NewHTTPFetcher(policy, cfg.Discovery.Targets)
		if e != nil {
			return nil, invalid
		}
	}
	if send {
		if len(input.AllowedOrigins) == 0 {
			return nil, invalid
		}
		cfg.Transport, e = federation.NewHTTPTransport(policy)
		if e != nil {
			return nil, invalid
		}
	}
	return cfg, nil
}

// Reject JSON's case-insensitive struct aliases, including duplicate logical
// options spelled with different casing. Configuration has one exact spelling.
func canonicalFederationFields(raw []byte) bool {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	names := []string{"public_admission", "incarnation", "retired_remote_actors", "retired_remote_key_ids", "retired_remote_key_fingerprints", "server_actor", "username", "name", "summary", "public_key_file", "discovery_targets", "resource", "actor", "origin", "allowed_origins", "max_attempts", "delivery_timeout_seconds", "local_key_custody", "key_directory", "operation_directory", "local_keys", "publisher_id", "private_key_file", "remote_keys", "inbox", "key_id"}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				for _, name := range names {
					if strings.EqualFold(key, name) && key != name {
						return false
					}
				}
				if !walk(value) {
					return false
				}
			}
		case []any:
			for _, value := range x {
				if !walk(value) {
					return false
				}
			}
		}
		return true
	}
	return walk(root)
}

// A standalone static provider explicitly identifies its known and retired pins.
// Operators must preserve retirement entries; durable admissions and peer rows
// retain their own reservations even when configuration changes.
func (k configuredFederationKeys) FirstContact(ctx context.Context, candidate federation.RemoteKey) (bool, error) {
	if e := ctx.Err(); e != nil {
		return false, e
	}
	if candidate.PublicKey != nil {
		fingerprint, e := federation.KeyFingerprint(candidate.PublicKey)
		if e != nil {
			return false, e
		}
		if k.retiredFingerprints[fingerprint] {
			return false, nil
		}
		for _, pin := range k.remote {
			other, e := federation.KeyFingerprint(pin.PublicKey)
			if e != nil {
				return false, e
			}
			if fingerprint == other {
				return false, nil
			}
		}
	}
	return k.actors[candidate.Actor].Actor == "" && k.remote[candidate.KeyID].KeyID == "" && !k.retiredActors[candidate.Actor] && !k.retiredKeys[candidate.KeyID], nil
}
