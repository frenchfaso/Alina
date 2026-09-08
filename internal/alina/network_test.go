package alina

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTermuxDNSConfig(t *testing.T) {
	servers, err := dnsServers("# local configuration\nsearch example.test\nnameserver 192.168.1.1 # router\nnameserver 2001:4860:4860::8888\nnameserver ::1\nnameserver 1.1.1.1\n")
	if err != nil || len(servers) != 3 || servers[0] != "192.168.1.1:53" || servers[1] != "[2001:4860:4860::8888]:53" {
		t.Fatal(servers, err)
	}
	for _, data := range []string{"", "nameserver example.com", "nameserver 1.1.1.1:53", strings.Repeat("x", 64<<10+1)} {
		if _, err := dnsServers(data); err == nil {
			t.Fatal("invalid DNS accepted")
		}
	}
	prefix := t.TempDir()
	r := hostResolver(prefix)
	if _, err := r.Dial(context.Background(), "udp", "127.0.0.1:53"); err == nil {
		t.Fatal("missing config selected a fallback DNS server")
	}
	if err := os.Mkdir(filepath.Join(prefix, "etc"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(prefix, "etc", "resolv.conf")
	if err := os.WriteFile(path, []byte("nameserver invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Dial(context.Background(), "udp", "127.0.0.1:53"); err == nil {
		t.Fatal("invalid config selected a fallback DNS server")
	}
	// A subsequent call sees a changed configuration, without restarting Alina.
	if err := os.WriteFile(path, []byte("nameserver 127.0.0.1"), 0600); err != nil {
		t.Fatal(err)
	}
	conn, err := r.Dial(context.Background(), "udp", "8.8.8.8:53")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.RemoteAddr().String() != "127.0.0.1:53" {
		t.Fatal(conn.RemoteAddr())
	}
}
