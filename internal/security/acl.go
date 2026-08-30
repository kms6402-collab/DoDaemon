package security

import (
	"fmt"
	"net"
)

// ACL is an IP allow/deny list matcher evaluated as: if an allowlist is
// configured, the address must match it; if the address matches the
// denylist, it is rejected regardless of the allowlist. An empty ACL
// (both lists empty) allows everything — callers implementing
// deny-by-default onboarding should treat that state as needing an
// explicit user confirmation (see docs/PLAN.md §8.6).
type ACL struct {
	allow []*net.IPNet
	deny  []*net.IPNet
}

// NewACL parses CIDR or bare-IP entries into an ACL. A bare IP "1.2.3.4"
// is treated as the single-host CIDR "1.2.3.4/32" (or /128 for IPv6).
func NewACL(allowList, denyList []string) (*ACL, error) {
	allow, err := parseNets(allowList)
	if err != nil {
		return nil, fmt.Errorf("allow_list: %w", err)
	}
	deny, err := parseNets(denyList)
	if err != nil {
		return nil, fmt.Errorf("deny_list: %w", err)
	}
	return &ACL{allow: allow, deny: deny}, nil
}

func parseNets(entries []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(entries))
	for _, e := range entries {
		_, ipNet, err := net.ParseCIDR(e)
		if err != nil {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP/CIDR %q: %w", e, err)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			ipNet = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
		}
		nets = append(nets, ipNet)
	}
	return nets, nil
}

// Allowed reports whether addr is permitted to connect under this ACL.
func (a *ACL) Allowed(addr net.IP) bool {
	if a == nil {
		return true
	}
	for _, n := range a.deny {
		if n.Contains(addr) {
			return false
		}
	}
	if len(a.allow) == 0 {
		return true
	}
	for _, n := range a.allow {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// IsOpen reports whether this ACL has no allowlist configured, meaning it
// currently permits any address that isn't explicitly denied.
func (a *ACL) IsOpen() bool {
	return a == nil || len(a.allow) == 0
}
