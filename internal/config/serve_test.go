package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePublicURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Mac.Example.ts.net":       "https://mac.example.ts.net",
		"https://mac.example.ts.net/":      "https://mac.example.ts.net",
		"https://mac.example.ts.net:8443":  "https://mac.example.ts.net:8443",
		"http://mac.example.ts.net":        "",
		"https://mac.example.ts.net/app":   "",
		"https://user@mac.example.ts.net":  "",
		"https://mac.example.ts.net/?x=1":  "",
		"https://mac.example.ts.net/#frag": "",
		"mac.example.ts.net":               "",
	} {
		got, err := ParsePublicURL(raw)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("%s: %q, %v", raw, got, err)
		}
	}
}

func TestListenMustBeLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:7433": true,
		"[::1]:7433":     true,
		"0.0.0.0:7433":   false,
		":7433":          false,
		"10.0.0.2:7433":  false,
		"localhost:7433": false,
		"127.0.0.1":      false,
	} {
		if err := CheckListen(addr); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

func TestServeSettingsAreValidated(t *testing.T) {
	if got := Default().Serve; got.Listen != DefaultListen || got.PublicURL != "" {
		t.Fatalf("defaults %+v", got)
	}
	for name, tc := range map[string]struct {
		text, err string
	}{
		"loopback port":     {text: "[serve]\nlisten = \"127.0.0.1:9000\"\n"},
		"public url":        {text: "[serve]\npublic-url = \"https://mac.example.ts.net\"\n"},
		"all interfaces":    {text: "[serve]\nlisten = \"0.0.0.0:7433\"\n", err: "serve.listen"},
		"plain http":        {text: "[serve]\npublic-url = \"http://mac.example.ts.net\"\n", err: "serve.public-url"},
		"unknown key":       {text: "[serve]\nport = 7433\n", err: "unknown setting"},
		"the opencode flag": {text: "[serve]\nopencode-untested = true\n", err: "unknown setting"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err := Load(path, true)
			if (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("error %v, want %q", err, tc.err)
			}
		})
	}
}
