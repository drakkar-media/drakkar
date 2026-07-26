// Package wireguard provides a provider-agnostic parser/validator for
// standard WireGuard .conf files and an in-process userspace tunnel built
// on golang.zx2c4.com/wireguard's netstack TUN, so Drakkar never needs a
// separate VPN container or host routing-table changes.
package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Peer is one [Peer] section of a parsed WireGuard configuration.
type Peer struct {
	PublicKeyHex        string
	PresharedKeyHex     string // empty if not present
	Endpoint            string // host:port, already resolved-format
	AllowedIPs          []netip.Prefix
	PersistentKeepalive int
}

// Config is a fully parsed, structurally validated WireGuard configuration.
// Key material is kept only as UAPI-ready hex strings; the original base64
// text is not retained here.
type Config struct {
	PrivateKeyHex string
	Address       []netip.Prefix
	DNS           []netip.Addr
	MTU           int
	Peers         []Peer
}

// Summary is the non-secret view of a Config safe to show in the UI or
// return from an API response. It never includes PrivateKey/PresharedKey.
type Summary struct {
	InterfaceAddress    []string `json:"interfaceAddress"`
	DNS                 []string `json:"dns,omitempty"`
	Endpoint            string   `json:"endpoint,omitempty"`
	AllowedIPs          []string `json:"allowedIps,omitempty"`
	PersistentKeepalive int      `json:"persistentKeepalive,omitempty"`
}

// Summary produces the non-secret Summary view of c, exposing only the
// first peer's endpoint/keepalive/allowed IPs since a multi-peer config has
// no single "the" endpoint to summarize.
func (c *Config) Summary() Summary {
	s := Summary{}
	for _, a := range c.Address {
		s.InterfaceAddress = append(s.InterfaceAddress, a.String())
	}
	for _, d := range c.DNS {
		s.DNS = append(s.DNS, d.String())
	}
	if len(c.Peers) > 0 {
		p := c.Peers[0]
		s.Endpoint = p.Endpoint
		s.PersistentKeepalive = p.PersistentKeepalive
		for _, ip := range p.AllowedIPs {
			s.AllowedIPs = append(s.AllowedIPs, ip.String())
		}
	}
	return s
}

// ParseConfig parses a standard WireGuard wg0.conf-style document
// ([Interface]/[Peer] sections, PrivateKey/Address/DNS/MTU and
// PublicKey/PresharedKey/Endpoint/AllowedIPs/PersistentKeepalive) and
// validates it structurally. It never logs or returns the raw text.
func ParseConfig(text string) (*Config, error) {
	cfg := &Config{MTU: 1420}
	var current *Peer
	inInterface := false

	haveInterface := false
	lines := strings.Split(text, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch section {
			case "interface":
				inInterface = true
				haveInterface = true
			case "peer":
				inInterface = false
				cfg.Peers = append(cfg.Peers, Peer{})
				current = &cfg.Peers[len(cfg.Peers)-1]
			default:
				return nil, fmt.Errorf("unsupported configuration: unknown section %q at line %d", line, i+1)
			}
			continue
		}
		key, value, ok := splitKV(line)
		if !ok {
			return nil, fmt.Errorf("unsupported configuration: malformed line %d", i+1)
		}
		if inInterface {
			if err := applyInterfaceField(cfg, key, value); err != nil {
				return nil, err
			}
		} else {
			if current == nil {
				return nil, fmt.Errorf("unsupported configuration: %q outside any section", key)
			}
			if err := applyPeerField(current, key, value); err != nil {
				return nil, err
			}
		}
	}

	if !haveInterface {
		return nil, fmt.Errorf("missing interface section")
	}
	if cfg.PrivateKeyHex == "" {
		return nil, fmt.Errorf("missing private key")
	}
	if len(cfg.Address) == 0 {
		return nil, fmt.Errorf("missing interface address")
	}
	if len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("no peer")
	}
	for i, p := range cfg.Peers {
		if p.PublicKeyHex == "" {
			return nil, fmt.Errorf("missing peer public key (peer %d)", i+1)
		}
		if p.Endpoint == "" {
			return nil, fmt.Errorf("invalid endpoint: peer %d has no endpoint", i+1)
		}
	}
	return cfg, nil
}

func splitKV(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, '=')
	if idx < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:idx]))
	value = strings.TrimSpace(line[idx+1:])
	return key, value, true
}

func applyInterfaceField(cfg *Config, key, value string) error {
	switch key {
	case "privatekey":
		hexKey, err := keyToHex(value)
		if err != nil {
			return fmt.Errorf("malformed private key: %w", err)
		}
		cfg.PrivateKeyHex = hexKey
	case "address":
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			prefix, err := parseCIDROrIP(part)
			if err != nil {
				return fmt.Errorf("missing interface address: invalid Address %q: %w", part, err)
			}
			cfg.Address = append(cfg.Address, prefix)
		}
	case "dns":
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			addr, err := netip.ParseAddr(part)
			if err != nil {
				return fmt.Errorf("invalid DNS address %q: %w", part, err)
			}
			cfg.DNS = append(cfg.DNS, addr)
		}
	case "mtu":
		mtu, err := strconv.Atoi(value)
		if err != nil || mtu <= 0 {
			return fmt.Errorf("unsupported configuration: invalid MTU %q", value)
		}
		cfg.MTU = mtu
	case "listenport", "table", "preup", "postup", "predown", "postdown", "saveconfig":
		// Recognized but not applicable to a userspace outbound-only tunnel.
	default:
		return fmt.Errorf("unsupported configuration: unknown Interface field %q", key)
	}
	return nil
}

func applyPeerField(p *Peer, key, value string) error {
	switch key {
	case "publickey":
		hexKey, err := keyToHex(value)
		if err != nil {
			return fmt.Errorf("malformed private key: peer public key: %w", err)
		}
		p.PublicKeyHex = hexKey
	case "presharedkey":
		hexKey, err := keyToHex(value)
		if err != nil {
			return fmt.Errorf("malformed private key: preshared key: %w", err)
		}
		p.PresharedKeyHex = hexKey
	case "endpoint":
		host, port, err := net.SplitHostPort(value)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("invalid endpoint %q", value)
		}
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("invalid endpoint %q: non-numeric port", value)
		}
		p.Endpoint = value
	case "allowedips":
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			prefix, err := parseCIDROrIP(part)
			if err != nil {
				return fmt.Errorf("invalid AllowedIPs: malformed CIDR %q: %w", part, err)
			}
			p.AllowedIPs = append(p.AllowedIPs, prefix)
		}
	case "persistentkeepalive":
		ka, err := strconv.Atoi(value)
		if err != nil || ka < 0 {
			return fmt.Errorf("unsupported configuration: invalid PersistentKeepalive %q", value)
		}
		p.PersistentKeepalive = ka
	default:
		return fmt.Errorf("unsupported configuration: unknown Peer field %q", key)
	}
	return nil
}

func parseCIDROrIP(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// keyToHex converts a standard WireGuard base64 32-byte key into the hex
// form the device's UAPI (IpcSet) expects.
func keyToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d bytes", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
