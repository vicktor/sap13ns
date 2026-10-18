package netguard

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{
		"93.184.215.14": true, "2606:4700::1111": true, "2a00:1450:4001:80b::200e": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.20.0.1": false, "192.168.1.1": false, "100.64.0.1": false,
		"169.254.169.254": false, "0.0.0.0": false, "255.255.255.255": false, "198.18.0.1": false,
		"::1": false, "::": false, "::ffff:127.0.0.1": false, "::127.0.0.1": false, "::a9fe:a9fe": false,
		"64:ff9b::a9fe:a9fe": false, "64:ff9b:1::1": false, "2002:7f00:1::1": false, "2001:0:4136:e378::1": false,
		"2001:2::1": false, "2001:db8::1": false, "3fff::1": false, "5f00::1": false,
		"fd00::1": false, "fe80::1": false, "fec0::1": false, "ff02::1": false,
	} {
		if got := Public(netip.MustParseAddr(s)); got != want {
			t.Errorf("Public(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestDialerRefusesLoopback(t *testing.T) {
	dial := Dialer(nil, nil, 0)
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("dial loopback: %v", err)
	}
	if _, err := dial(context.Background(), "tcp", "[::ffff:10.0.0.1]:80"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("dial mapped private: %v", err)
	}
}
