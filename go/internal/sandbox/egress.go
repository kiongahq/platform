package sandbox

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

// blockedPrefixes are destinations a git helper must never be sent to: loopback,
// private, link-local, carrier-grade NAT, documentation, benchmarking, multicast and
// reserved space, plus translation ranges that can carry one of those inside. Cloud
// metadata mostly sits in link-local space; the rest is listed explicitly.
var blockedPrefixes = func() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, raw := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"168.63.129.16/32", // Azure platform services
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001:db8::/32", "2002::/16",
		"fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		prefixes = append(prefixes, netip.MustParsePrefix(raw))
	}
	return prefixes
}()

// blockedAddress reports whether an address is not a public destination. IPv4-mapped
// IPv6 is unwrapped first, so ::ffff:10.0.0.1 is judged as 10.0.0.1.
func blockedAddress(address netip.Addr) bool {
	address = address.Unmap().WithZone("")
	if !address.IsValid() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// resolveLookup is the real resolver. Service.resolve replaces it in tests.
func resolveLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// pinRepositoryHost resolves the allowed host once, here, and refuses if any answer is
// not public. One bad answer among good ones refuses the lot, because git could pick any
// of them. The answers become the helper's only name resolution, so a rebinding answer
// served after this check never reaches git. Hostnames only: an IP literal has no lookup
// to pin and is judged directly.
func (s *Service) pinRepositoryHost(ctx context.Context, repository string) ([]HostEntry, error) {
	refused := &client.Error{Status: http.StatusForbidden, Code: client.CodeHostNotAllowed, Message: "repository host resolves to a private or reserved address"}
	parsed, err := url.Parse(repository)
	if err != nil {
		return nil, invalid("repository is malformed")
	}
	host := parsed.Hostname()
	if literal, err := netip.ParseAddr(host); err == nil {
		if blockedAddress(literal) && !s.allowPrivateForTests() {
			return nil, refused
		}
		return nil, nil
	}
	addresses, err := s.resolve(ctx, host)
	if err != nil || len(addresses) == 0 {
		s.logger.Printf("sandbox: could not resolve an allowed git host: %v", err)
		return nil, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the repository host could not be resolved from the sandbox manager"}
	}
	entries := make([]HostEntry, 0, len(addresses))
	for _, address := range addresses {
		if blockedAddress(address) && !s.allowPrivateForTests() {
			return nil, refused
		}
		entries = append(entries, HostEntry{Host: host, Address: address.Unmap().WithZone("").String()})
	}
	return entries, nil
}

func (s *Service) allowPrivateForTests() bool {
	if s.config.GitAllowPrivateForTests {
		s.logger.Printf("WARNING: KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS=1: a git helper may reach a private address. Tests only.")
		return true
	}
	return false
}
