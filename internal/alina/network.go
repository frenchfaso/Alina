package alina

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func termuxPrefix() string {
	if runtime.GOOS != "android" {
		return ""
	}
	prefix := os.Getenv("PREFIX")
	if filepath.IsAbs(prefix) {
		return prefix
	}
	return ""
}

// Cross-built pure-Go binaries do not include Termux's patched /etc paths.
// Use its installed DNS configuration and CA bundle, without changing the host
// or choosing a public resolver on the user's behalf.
func hostResolver(prefix string) *net.Resolver {
	if prefix == "" {
		return net.DefaultResolver
	}
	var next atomic.Uint64
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		data, err := os.ReadFile(filepath.Join(prefix, "etc", "resolv.conf"))
		if err != nil {
			return nil, errors.New("Termux DNS configuration unavailable: check PREFIX/etc/resolv.conf")
		}
		servers, err := dnsServers(string(data))
		if err != nil {
			return nil, err
		}
		server := servers[(next.Add(1)-1)%uint64(len(servers))]
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, server)
	}}
}

func dnsServers(config string) ([]string, error) {
	if len(config) > 64<<10 {
		return nil, errors.New("Termux DNS configuration exceeds 64 KiB")
	}
	var servers []string
	for _, line := range strings.Split(config, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			if ip, err := netip.ParseAddr(fields[1]); err == nil {
				servers = append(servers, net.JoinHostPort(ip.String(), "53"))
				if len(servers) == 3 {
					break
				}
			}
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("no DNS nameserver IP in PREFIX/etc/resolv.conf")
	}
	return servers, nil
}

var hostHTTPTransport = sync.OnceValue(func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	prefix := termuxPrefix()
	if prefix == "" {
		return transport
	}
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Resolver: hostResolver(prefix)}).DialContext
	// Respect explicit Go trust-store overrides. Otherwise add Termux's CA
	// bundle to the normal Android roots; TLS verification remains enabled.
	if os.Getenv("SSL_CERT_FILE") == "" && os.Getenv("SSL_CERT_DIR") == "" {
		if pem, err := os.ReadFile(filepath.Join(prefix, "etc", "tls", "cert.pem")); err == nil {
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if roots.AppendCertsFromPEM(pem) {
				transport.TLSClientConfig = &tls.Config{RootCAs: roots}
			}
		}
	}
	return transport
})
