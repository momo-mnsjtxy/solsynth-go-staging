// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/pushprovider"
)

type pushDeviceSettings struct{ project, credentialEnv, keyEnv *string }

func pushDeviceFlags(f *flag.FlagSet) pushDeviceSettings {
	return pushDeviceSettings{f.String("push-fcm-project", "", "explicit Firebase project for proof-verified notification devices"), f.String("push-fcm-service-account-env", "", "environment variable with FCM service account JSON"), f.String("push-device-key-env", "SOLSYNTH_PUSH_DEVICE_KEY", "environment variable with base64 AES-256 device encryption key")}
}
func (s pushDeviceSettings) apply(c *application.Config) error {
	if *s.project == "" && *s.credentialEnv == "" {
		return nil
	}
	invalid := errors.New("invalid explicit FCM device configuration")
	if *s.project == "" || *s.credentialEnv == "" {
		return invalid
	}
	raw := os.Getenv(*s.credentialEnv)
	if len(raw) > 64<<10 {
		return invalid
	}
	var credentials struct {
		Type  string `json:"type"`
		Email string `json:"client_email"`
		Key   string `json:"private_key"`
	}
	if json.Unmarshal([]byte(raw), &credentials) != nil || credentials.Type != "service_account" {
		return invalid
	}
	provider, e := pushprovider.NewFCM(pushprovider.FCMConfig{ProjectID: *s.project, ServiceAccountEmail: credentials.Email, PrivateKeyPEM: credentials.Key}, nil)
	if e != nil {
		return invalid
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv(*s.keyEnv))
	if e != nil || len(key) != 32 {
		return invalid
	}
	c.NotificationDevices = &notificationpg.DeviceConfig{EncryptionKey: key, Provider: provider}
	return nil
}
