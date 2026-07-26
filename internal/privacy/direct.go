package privacy

import (
	"context"
	"net"
	"time"
)

// DirectDialer dials the normal host network path — today's behavior,
// unchanged, used whenever privacy.mode is "direct".
type DirectDialer struct {
	Timeout time.Duration
}

// NewDirectDialer creates a DirectDialer that applies timeout to every dial.
func NewDirectDialer(timeout time.Duration) *DirectDialer {
	return &DirectDialer{Timeout: timeout}
}

// DialContext dials address directly on the host network, honoring d.Timeout.
func (d *DirectDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: d.Timeout}
	return dialer.DialContext(ctx, network, address)
}
