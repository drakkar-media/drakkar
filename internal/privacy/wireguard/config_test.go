package wireguard

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func genKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func validConf(t *testing.T) string {
	t.Helper()
	return "[Interface]\n" +
		"PrivateKey = " + genKey(t) + "\n" +
		"Address = 10.6.0.2/32\n" +
		"DNS = 10.6.0.1\n" +
		"MTU = 1420\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + genKey(t) + "\n" +
		"AllowedIPs = 0.0.0.0/0, ::/0\n" +
		"Endpoint = vpn.example.com:51820\n" +
		"PersistentKeepalive = 25\n"
}

func TestParseConfigValid(t *testing.T) {
	cfg, err := ParseConfig(validConf(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Address) != 1 || cfg.Address[0].Addr().String() != "10.6.0.2" {
		t.Fatalf("unexpected address: %+v", cfg.Address)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(cfg.Peers))
	}
	p := cfg.Peers[0]
	if p.Endpoint != "vpn.example.com:51820" {
		t.Fatalf("unexpected endpoint: %s", p.Endpoint)
	}
	if p.PersistentKeepalive != 25 {
		t.Fatalf("unexpected keepalive: %d", p.PersistentKeepalive)
	}
	if len(p.AllowedIPs) != 2 {
		t.Fatalf("expected 2 allowed IPs, got %d", len(p.AllowedIPs))
	}

	summary := cfg.Summary()
	if summary.Endpoint != p.Endpoint {
		t.Fatalf("summary endpoint mismatch")
	}
}

func TestParseConfigValidationErrors(t *testing.T) {
	goodKey := genKey(t)
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "missing private key",
			text: "[Interface]\nAddress = 10.6.0.2/32\n[Peer]\nPublicKey = " + goodKey + "\nEndpoint = h:1\nAllowedIPs = 0.0.0.0/0\n",
			want: "missing private key",
		},
		{
			name: "malformed private key",
			text: "[Interface]\nPrivateKey = not-base64!!\nAddress = 10.6.0.2/32\n[Peer]\nPublicKey = " + goodKey + "\nEndpoint = h:1\n",
			want: "malformed private key",
		},
		{
			name: "no peer",
			text: "[Interface]\nPrivateKey = " + goodKey + "\nAddress = 10.6.0.2/32\n",
			want: "no peer",
		},
		{
			name: "missing peer public key",
			text: "[Interface]\nPrivateKey = " + goodKey + "\nAddress = 10.6.0.2/32\n[Peer]\nEndpoint = h:1\n",
			want: "missing peer public key",
		},
		{
			name: "invalid endpoint",
			text: "[Interface]\nPrivateKey = " + goodKey + "\nAddress = 10.6.0.2/32\n[Peer]\nPublicKey = " + goodKey + "\nEndpoint = not-an-endpoint\n",
			want: "invalid endpoint",
		},
		{
			name: "missing interface address",
			text: "[Interface]\nPrivateKey = " + goodKey + "\n[Peer]\nPublicKey = " + goodKey + "\nEndpoint = h:1\n",
			want: "missing interface address",
		},
		{
			name: "malformed CIDR",
			text: "[Interface]\nPrivateKey = " + goodKey + "\nAddress = 10.6.0.2/32\n[Peer]\nPublicKey = " + goodKey + "\nEndpoint = h:1\nAllowedIPs = not-a-cidr\n",
			want: "malformed CIDR",
		},
		{
			name: "unsupported configuration",
			text: "[Bogus]\nfoo = bar\n",
			want: "unsupported configuration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig(tc.text)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestParseConfigNeverReturnsRawKeysAsBase64(t *testing.T) {
	cfg, err := ParseConfig(validConf(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PrivateKeyHex) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(cfg.PrivateKeyHex))
	}
	// Summary must never carry key material.
	summary := cfg.Summary()
	_ = summary
}
