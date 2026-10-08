// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"net/netip"
	"os"
	"solsynth.local/go-migration/ports/passport/ipcheck"
	"strings"
)

type ipCheckSettings struct{ dataset, proxies, header *string }

func ipCheckFlags(f *flag.FlagSet) ipCheckSettings {
	return ipCheckSettings{f.String("ipcheck-geo-file", "", "explicit local CIDR geo JSON with provenance; no bundled data or network lookup"), f.String("ipcheck-trusted-proxies", "", "explicit comma-separated trusted proxy CIDRs"), f.String("ipcheck-forwarded-header", "", "explicit X-Forwarded-For, X-Real-IP or CF-Connecting-IP policy")}
}
func (s ipCheckSettings) config() (*ipcheck.Config, error) {
	c := &ipcheck.Config{ForwardedHeader: *s.header}
	if *s.proxies != "" {
		for _, raw := range strings.Split(*s.proxies, ",") {
			p, e := netip.ParsePrefix(strings.TrimSpace(raw))
			if e != nil {
				return nil, errors.New("invalid IP-check proxy CIDR")
			}
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}
	if _, e := ipcheck.New(*c, nil); e != nil {
		return nil, e
	}
	if *s.dataset != "" {
		f, e := os.Open(*s.dataset)
		if e != nil {
			return nil, errors.New("IP-check dataset unavailable")
		}
		defer f.Close()
		c.Provider, e = ipcheck.DecodeDataset(f)
		if e != nil {
			return nil, errors.New("invalid IP-check dataset")
		}
	}
	return c, nil
}
