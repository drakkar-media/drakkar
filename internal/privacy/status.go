package privacy

// Status is the read-only runtime view of the active privacy route,
// returned by GET /api/settings/privacy/status. Never carries secrets.
type Status struct {
	Mode             Mode             `json:"mode"`
	State            string           `json:"status"` // direct|connecting|connected|disconnected|error|reachable|unreachable
	ProtectedTraffic []string         `json:"protectedTraffic"`
	Endpoint         string           `json:"endpoint,omitempty"`
	Error            string           `json:"error,omitempty"`
	WireGuard        *WireGuardStatus `json:"wireguard,omitempty"`
}

// WireGuardStatus is the sanitized (no key material) WireGuard summary.
type WireGuardStatus struct {
	InterfaceAddress    []string `json:"interfaceAddress,omitempty"`
	DNS                 []string `json:"dns,omitempty"`
	Endpoint            string   `json:"endpoint,omitempty"`
	AllowedIPs          []string `json:"allowedIps,omitempty"`
	PersistentKeepalive int      `json:"persistentKeepalive,omitempty"`
}

var protectedTraffic = []string{"usenet", "indexers"}
