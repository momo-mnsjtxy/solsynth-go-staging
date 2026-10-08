// SPDX-License-Identifier: AGPL-3.0-only
package domaintrust

import (
	"golang.org/x/net/idna"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

type Policy struct {
	AllowHTTP, AllowIPAddresses, AllowPrivateNetworks bool
	BlockedPorts                                      []int
}
type Address struct {
	Host, Scheme string
	Port         *int
	IP           netip.Addr
}

func DefaultBlockedPorts() []int {
	return []int{21, 22, 23, 25, 53, 110, 143, 993, 995, 3306, 5432, 6379, 27017}
}
func control(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
func NormalizeHost(raw string) (string, netip.Addr, error) {
	if raw == "" || len(raw) > 1024 || !utf8.ValidString(raw) || control(raw) || strings.ContainsAny(raw, " /\\@?#%") {
		return "", netip.Addr{}, ErrInvalid
	}
	host := strings.TrimSuffix(strings.ToLower(raw), ".")
	if ip, e := netip.ParseAddr(host); e == nil {
		ip = ip.Unmap()
		return ip.String(), ip, nil
	}
	if strings.Contains(host, ":") {
		return "", netip.Addr{}, ErrInvalid
	}
	ascii, e := idna.Lookup.ToASCII(host)
	if e != nil || len(ascii) > 253 || ascii == "" {
		return "", netip.Addr{}, ErrInvalid
	}
	numeric := true
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", netip.Addr{}, ErrInvalid
		}
		number := true
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", netip.Addr{}, ErrInvalid
			}
			if c < '0' || c > '9' {
				number = false
			}
		}
		if strings.HasPrefix(label, "0x") {
			if _, x := strconv.ParseUint(label[2:], 16, 64); x == nil {
				number = true
			}
		}
		numeric = numeric && number
	}
	if numeric {
		return "", netip.Addr{}, ErrInvalid
	}
	return ascii, netip.Addr{}, nil
}
func Parse(raw string) (Address, error) {
	var out Address
	if raw == "" || len(raw) > 8192 || !utf8.ValidString(raw) || control(raw) || strings.Contains(raw, "\\") {
		return out, ErrInvalid
	}
	u, e := url.Parse(raw)
	if e != nil || u.Opaque != "" || u.User != nil || u.Host == "" {
		return out, ErrInvalid
	}
	out.Scheme = strings.ToLower(u.Scheme)
	if out.Scheme != "http" && out.Scheme != "https" {
		return out, ErrInvalid
	}
	for _, component := range []string{u.EscapedPath(), u.RawQuery, u.EscapedFragment()} {
		v, e := url.PathUnescape(component)
		if e != nil || control(v) {
			return out, ErrInvalid
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return out, ErrInvalid
	}
	host := u.Hostname()
	bracketed := strings.HasPrefix(u.Host, "[")
	out.Host, out.IP, e = NormalizeHost(host)
	if e != nil {
		return out, ErrInvalid
	}
	if bracketed && (!out.IP.IsValid() || !strings.Contains(host, ":")) {
		return out, ErrInvalid
	}
	if !bracketed && strings.Contains(host, ":") {
		return out, ErrInvalid
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return out, ErrInvalid
		}
		if !(out.Scheme == "http" && n == 80 || out.Scheme == "https" && n == 443) {
			out.Port = &n
		}
	}
	return out, nil
}
func ptr(s string) *string { return &s }
func (p Policy) Default(a Address) *Result {
	if a.Scheme == "http" && !p.AllowHTTP {
		return &Result{BlockReason: ptr("HTTP protocol is blocked by default (not secure)"), MatchedSource: ptr("default_http_block")}
	}
	if a.IP.IsValid() {
		if !p.AllowPrivateNetworks && (a.IP.IsPrivate() || a.IP.IsLoopback() || a.IP.IsLinkLocalUnicast() || a.IP.IsLinkLocalMulticast() || a.IP.IsMulticast() || a.IP.IsUnspecified()) {
			return &Result{BlockReason: ptr("Private network addresses are blocked by default"), MatchedSource: ptr("default_private_network_block")}
		}
		if !p.AllowIPAddresses {
			return &Result{BlockReason: ptr("Direct IP addresses are blocked by default"), MatchedSource: ptr("default_ip_block")}
		}
	}
	ports := p.BlockedPorts
	if ports == nil {
		ports = DefaultBlockedPorts()
	}
	if a.Port != nil {
		for _, port := range ports {
			if port == *a.Port {
				return &Result{BlockReason: ptr("Port is blocked by default"), MatchedSource: ptr("default_port_block")}
			}
		}
	}
	return nil
}
func ValidateRule(r Rule) (Rule, error) {
	r.DomainPattern = strings.ToLower(strings.TrimSpace(r.DomainPattern))
	if r.DomainPattern == "" || len(r.DomainPattern) > 512 || r.Priority < -1000000 || r.Priority > 1000000 || r.TrustLevel < Blocked || r.TrustLevel > Verified {
		return r, ErrInvalid
	}
	for _, c := range r.DomainPattern {
		if c > 127 || !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-.:*?", c)) {
			return r, ErrInvalid
		}
	}
	if r.Protocol != nil {
		v := strings.ToLower(strings.TrimSpace(*r.Protocol))
		if v != "" && v != "http" && v != "https" {
			return r, ErrInvalid
		}
		if v == "" {
			r.Protocol = nil
		} else {
			r.Protocol = &v
		}
	}
	if r.PortRestriction != nil && (*r.PortRestriction < 1 || *r.PortRestriction > 65535) {
		return r, ErrInvalid
	}
	if r.Reason != nil && (!utf8.ValidString(*r.Reason) || control(*r.Reason) || len(utf16.Encode([]rune(*r.Reason))) > 256) {
		return r, ErrInvalid
	}
	return r, nil
}
func Matches(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" || host == "" || len(pattern) > 512 || len(host) > 253 {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "**.") {
		base := pattern[3:]
		return host == base || strings.HasSuffix(host, "."+base)
	}
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(host, "."+pattern[2:])
	}
	if !strings.Contains(pattern, "*") {
		return pattern == host
	}
	prev := make([]bool, len(host)+1)
	prev[0] = true
	for _, p := range []byte(pattern) {
		next := make([]bool, len(host)+1)
		if p == '*' {
			next[0] = prev[0]
		}
		for j := 1; j <= len(host); j++ {
			if p == '*' {
				next[j] = prev[j] || next[j-1]
			} else if p == '?' || p == host[j-1] {
				next[j] = prev[j-1]
			}
		}
		prev = next
	}
	return prev[len(host)]
}
func EvaluateRules(a Address, p Policy, rules []Rule) (Result, error) {
	if result := p.Default(a); result != nil {
		return *result, nil
	}
	work := 0
	for _, rule := range rules {
		if !rule.IsActive {
			continue
		}
		if rule.Protocol != nil && *rule.Protocol != a.Scheme {
			continue
		}
		if rule.PortRestriction != nil && (a.Port == nil || *a.Port != *rule.PortRestriction) {
			continue
		}
		work += len(rule.DomainPattern) * len(a.Host)
		if work > 8<<20 {
			return Result{}, ErrUnavailable
		}
		if !Matches(rule.DomainPattern, a.Host) {
			continue
		}
		out := Result{IsAllowed: rule.TrustLevel != Blocked, IsVerified: rule.TrustLevel == Verified, MatchedSource: ptr("database_rule")}
		if !out.IsAllowed {
			out.BlockReason = rule.Reason
			if out.BlockReason == nil {
				out.BlockReason = ptr("Domain is blocked")
			}
		}
		return out, nil
	}
	return Result{IsAllowed: true}, nil
}
func InvalidResult() Result { return Result{BlockReason: ptr("Invalid URL format")} }
