// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"os"
	"solsynth.local/go-migration/internal/tencenttranslation"
	"solsynth.local/go-migration/internal/translationpg"
	"strings"
)

type translationSettings struct {
	enabled, send                        *bool
	subscriptions, idEnv, keyEnv, region *string
	project                              *int64
}

func translationFlags(f *flag.FlagSet) translationSettings {
	return translationSettings{f.Bool("enable-translation", false, "enable owned paid-grant translation"), f.Bool("translation-send", false, "explicitly enable configured Tencent outbound translation"), f.String("translation-subscriptions", "", "eligible canonical system subscription identifiers"), f.String("translation-secret-id-env", "", "explicit operator secret ID environment name"), f.String("translation-secret-key-env", "", "explicit operator secret key environment name"), f.String("translation-region", "", "explicit Tencent TMT region"), f.Int64("translation-project", 0, "Tencent TMT project ID")}
}
func (s translationSettings) config() (*translationpg.Config, error) {
	if !*s.enabled {
		if *s.send || *s.subscriptions != "" || *s.idEnv != "" || *s.keyEnv != "" || *s.region != "" || *s.project != 0 {
			return nil, errors.New("translation settings require explicit enablement")
		}
		return nil, nil
	}
	ids := strings.Split(*s.subscriptions, ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	gate, e := translationpg.NewSystemSubscriptions(ids)
	if e != nil {
		return nil, e
	}
	c := &translationpg.Config{Entitlement: gate}
	if !*s.send {
		if *s.idEnv != "" || *s.keyEnv != "" || *s.region != "" || *s.project != 0 {
			return nil, errors.New("translation provider settings require explicit send opt-in")
		}
		return c, nil
	}
	if *s.idEnv == "" || *s.keyEnv == "" || *s.idEnv == *s.keyEnv {
		return nil, errors.New("distinct explicit translation credential environment names required")
	}
	key := []byte(os.Getenv(*s.keyEnv))
	defer clear(key)
	c.Provider, e = tencenttranslation.New(tencenttranslation.Config{SecretID: os.Getenv(*s.idEnv), SecretKey: key, Region: *s.region, ProjectID: *s.project})
	if e != nil {
		return nil, errors.New("configured translation provider unavailable")
	}
	return c, nil
}
