// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
	"strings"
)

type ticketOptions struct {
	groups, required              *string
	completion, memberPermissions *string
	activation, verified          *bool
}

func ticketFlags(f *flag.FlagSet) ticketOptions {
	return ticketOptions{
		completion:        f.String("activation-policy-id", "", "explicit once-only canonical activation policy ID; requires verified contact"),
		memberPermissions: f.String("activation-member-permissions", "", "explicit allowlisted member permission keys, comma separated; no default grants"),
		groups:            f.String("exam-group-mappings", "", "explicit exam key=existing canonical group UUID mappings, comma separated"),
		required:          f.String("exam-required-tests", "", "explicit required test keys for activation requests, comma separated"),
		activation:        f.Bool("exam-activation-policy", false, "enable pending activation eligibility checks; never enables an account"),
		verified:          f.Bool("exam-require-verified-contact", true, "require canonical verified email for activation eligibility"),
	}
}
func (o ticketOptions) config() (*ticketpg.Config, error) {
	cfg := &ticketpg.Config{GrantedGroups: map[string]string{}}
	if len(*o.groups) > 16384 || len(*o.required) > 4096 {
		return nil, errors.New("exam policy too large")
	}
	if *o.groups != "" {
		for _, entry := range strings.Split(*o.groups, ",") {
			key, id, ok := strings.Cut(strings.TrimSpace(entry), "=")
			key = strings.TrimSpace(key)
			id = strings.TrimSpace(id)
			if !ok || !tickets.ValidKey(key) || !authority.ValidID(id) || cfg.GrantedGroups[key] != "" || len(cfg.GrantedGroups) >= 100 {
				return nil, errors.New("invalid exam group mapping")
			}
			cfg.GrantedGroups[key] = id
		}
	}
	if !*o.activation {
		if *o.required != "" || *o.completion != "" || *o.memberPermissions != "" {
			return nil, errors.New("completion and required tests need explicit activation policy")
		}
		return cfg, nil
	}
	cfg.Activation = &ticketpg.ActivationPolicy{RequireVerifiedContact: *o.verified}
	seen := map[string]bool{}
	if *o.required != "" {
		for _, key := range strings.Split(*o.required, ",") {
			key = strings.TrimSpace(key)
			lower := strings.ToLower(key)
			if !tickets.ValidKey(key) || seen[lower] || len(seen) >= 32 {
				return nil, errors.New("invalid required exam key")
			}
			seen[lower] = true
			cfg.Activation.RequiredTestKeys = append(cfg.Activation.RequiredTestKeys, key)
		}
	}
	if len(cfg.Activation.RequiredTestKeys) == 0 && !cfg.Activation.RequireVerifiedContact {
		return nil, errors.New("activation policy needs a verified requirement")
	}
	if *o.completion != "" {
		completion := &ticketpg.ActivationCompletion{PolicyID: *o.completion, Permissions: []string{}}
		if *o.memberPermissions != "" {
			completion.Permissions = strings.Split(*o.memberPermissions, ",")
		}
		if err := ticketpg.ValidateActivationCompletion(*completion); err != nil || !*o.verified {
			return nil, errors.New("invalid canonical activation completion policy")
		}
		cfg.Activation.Completion = completion
	} else if *o.memberPermissions != "" {
		return nil, errors.New("member permissions require explicit completion policy")
	}
	return cfg, nil
}
