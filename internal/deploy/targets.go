// Package deploy installs OpsRamp agents onto remote VMs over SSH (no Ansible
// or external tooling required — a self-contained Go SSH fan-out).
package deploy

import (
	"fmt"
	"net/netip"
	"strings"
)

// MaxTargets caps how many hosts a single deployment may fan out to.
const MaxTargets = 1024

// ExpandTargets parses a target spec into a de-duplicated host list. Tokens may
// be separated by commas, whitespace, or newlines, and each token may be:
//   - a single IP or hostname            (10.0.0.5, host.example.com)
//   - a CIDR block                       (10.0.0.0/28)
//   - a dashed range                     (10.0.0.10-10.0.0.20 or 10.0.0.10-20)
func ExpandTargets(spec string) ([]string, error) {
	fields := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == ';'
	})

	seen := map[string]bool{}
	var out []string
	add := func(h string) error {
		if h == "" || seen[h] {
			return nil
		}
		seen[h] = true
		out = append(out, h)
		if len(out) > MaxTargets {
			return fmt.Errorf("too many targets (limit %d)", MaxTargets)
		}
		return nil
	}

	for _, tok := range fields {
		switch {
		case strings.Contains(tok, "/"):
			hosts, err := expandCIDR(tok)
			if err != nil {
				return nil, err
			}
			for _, h := range hosts {
				if err := add(h); err != nil {
					return nil, err
				}
			}
		case strings.Contains(tok, "-"):
			hosts, err := expandRange(tok)
			if err != nil {
				return nil, err
			}
			for _, h := range hosts {
				if err := add(h); err != nil {
					return nil, err
				}
			}
		default:
			if err := add(tok); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no targets parsed from spec")
	}
	return out, nil
}

func expandCIDR(tok string) ([]string, error) {
	p, err := netip.ParsePrefix(tok)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", tok, err)
	}
	p = p.Masked()
	var hosts []string
	bits := p.Bits()
	isV4 := p.Addr().Is4()
	// Whether to drop the network and broadcast addresses (only for larger v4 blocks).
	dropEnds := isV4 && bits <= 30

	for addr := p.Addr(); p.Contains(addr); addr = addr.Next() {
		if dropEnds {
			if addr == p.Addr() {
				continue // network address
			}
			if !p.Contains(addr.Next()) {
				continue // broadcast address (last in range)
			}
		}
		hosts = append(hosts, addr.String())
		if len(hosts) > MaxTargets {
			return nil, fmt.Errorf("CIDR %q expands beyond limit %d", tok, MaxTargets)
		}
	}
	return hosts, nil
}

func expandRange(tok string) ([]string, error) {
	lo, hiRaw, ok := strings.Cut(tok, "-")
	if !ok {
		return nil, fmt.Errorf("invalid range %q", tok)
	}
	start, err := netip.ParseAddr(strings.TrimSpace(lo))
	if err != nil {
		return nil, fmt.Errorf("invalid range start %q: %w", lo, err)
	}
	hiRaw = strings.TrimSpace(hiRaw)

	var end netip.Addr
	if strings.Contains(hiRaw, ".") || strings.Contains(hiRaw, ":") {
		end, err = netip.ParseAddr(hiRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid range end %q: %w", hiRaw, err)
		}
	} else {
		// Short form: replace the final octet of the start address.
		if !start.Is4() {
			return nil, fmt.Errorf("short range form requires IPv4 start: %q", tok)
		}
		b := start.As4()
		var last int
		if _, err := fmt.Sscanf(hiRaw, "%d", &last); err != nil || last < 0 || last > 255 {
			return nil, fmt.Errorf("invalid range end octet %q", hiRaw)
		}
		b[3] = byte(last)
		end = netip.AddrFrom4(b)
	}

	if end.Less(start) {
		return nil, fmt.Errorf("range end before start: %q", tok)
	}
	var hosts []string
	for a := start; ; a = a.Next() {
		hosts = append(hosts, a.String())
		if a == end {
			break
		}
		if len(hosts) > MaxTargets {
			return nil, fmt.Errorf("range %q expands beyond limit %d", tok, MaxTargets)
		}
	}
	return hosts, nil
}
