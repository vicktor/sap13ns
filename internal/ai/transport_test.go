package ai

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"sap13ns/internal/netguard"
)

// The default image download client dials only public addresses, so a
// provider cannot point it at an internal host through a DNS name.
func TestDownloadDialsPublicOnly(t *testing.T) {
	dial := func(allowLocal bool, addr string) error {
		tr := newTransport(Config{AllowInsecureLocal: allowLocal}, nil).download.Transport.(*http.Transport)
		conn, err := tr.DialContext(context.Background(), "tcp", addr)
		if err == nil {
			conn.Close()
		}
		return err
	}
	for _, addr := range []string{"127.0.0.1:1", "10.0.0.1:443", "[::1]:1", "169.254.169.254:80"} {
		if err := dial(false, addr); !errors.Is(err, netguard.ErrForbidden) {
			t.Errorf("dial %s: %v", addr, err)
		}
	}
	if err := dial(true, "127.0.0.1:1"); errors.Is(err, netguard.ErrForbidden) {
		t.Error("loopback refused with AllowInsecureLocal")
	}
	if err := dial(true, "10.0.0.1:443"); !errors.Is(err, netguard.ErrForbidden) {
		t.Errorf("private address with AllowInsecureLocal: %v", err)
	}
}
