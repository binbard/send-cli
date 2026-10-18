package termux

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Configure uses Termux's DNS and TLS configuration for a static send binary.
// Android has no /etc/resolv.conf or standard Linux CA path for pure Go code.
func Configure() {
	home, err := os.UserHomeDir()
	if err != nil || !strings.HasSuffix(filepath.ToSlash(home), "/com.termux/files/home") {
		return
	}
	configDir := filepath.Join(filepath.Dir(home), "usr", "etc")
	path := filepath.Join(configDir, "resolv.conf")
	if os.Getenv("SSL_CERT_FILE") == "" && os.Getenv("SSL_CERT_DIR") == "" {
		cert := filepath.Join(configDir, "tls", "cert.pem")
		if _, err := os.Stat(cert); err == nil {
			_ = os.Setenv("SSL_CERT_FILE", cert)
		}
	}
	var next atomic.Uint64
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			servers, err := nameservers(path)
			if err != nil {
				return nil, err
			}
			server := servers[(next.Add(1)-1)%uint64(len(servers))]
			return (&net.Dialer{}).DialContext(ctx, network, server)
		},
	}
}

func nameservers(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read Termux DNS configuration %s: %w", path, err)
	}
	defer file.Close()
	var servers []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if ip := net.ParseIP(fields[1]); ip != nil {
			servers = append(servers, net.JoinHostPort(ip.String(), "53"))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Termux DNS configuration %s: %w", path, err)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("Termux DNS configuration %s has no valid nameserver", path)
	}
	return servers, nil
}
