package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// DefaultListen is where agentflockr serve listens. It is on loopback only,
// and a fixed port keeps `tailscale serve --bg 7433` stable between runs.
const DefaultListen = "127.0.0.1:7433"

// Serve holds the settings of agentflockr serve. --opencode-untested is not
// among them: enabling answers on an OpenCode release agentflockr was not
// tested with is a choice made at each start (REQ-CLI-011).
type Serve struct {
	// Listen is the TCP address of the phone app and its API. It must be on
	// loopback: the phone reaches it through tailscale serve, never directly.
	Listen string `toml:"listen"`
	// PublicURL is the https origin the phone uses. Empty means the name
	// Tailscale reports for this machine.
	PublicURL string `toml:"public-url,omitempty"`
}

func (s Serve) validate() error {
	if err := CheckListen(s.Listen); err != nil {
		return fmt.Errorf("serve.listen: %w", err)
	}
	if s.PublicURL != "" {
		if _, err := ParsePublicURL(s.PublicURL); err != nil {
			return fmt.Errorf("serve.public-url: %w", err)
		}
	}
	return nil
}

// CheckListen accepts only loopback addresses: the phone reaches serve
// through Tailscale Serve, never directly.
func CheckListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("serve listens on loopback only; expose it with tailscale serve (%q is not a loopback address)", addr)
	}
	return nil
}

// ParsePublicURL validates an https origin, such as
// https://mac.example.ts.net, and returns it in canonical form.
func ParsePublicURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the public URL must be an https origin such as https://mac.example.ts.net, not %q", raw)
	}
	return "https://" + strings.ToLower(u.Host), nil
}
