package config

import (
	"strings"
	"testing"
)

func previewConfig() *Config {
	return &Config{
		Server: Server{Bind: "127.0.0.1:8837", AllowedOrigins: []string{"https://deck.example.com"}},
		Auth:   Auth{Mode: "password"},
		ProjectPreviews: ProjectPreviews{Bind: "127.0.0.1:8840", DeckOrigin: "https://deck.example.com", Unconfined: true, Targets: []PreviewTarget{
			{ProjectID: "one", Origin: "https://one.example.net", Upstream: "127.0.0.1:5173"},
			{ProjectID: "two", Origin: "https://two.example.net", Upstream: "127.0.0.1:5174"},
		}},
	}
}

func managedPreviewConfig() *Config {
	c := previewConfig()
	c.ProjectPreviews.Targets = nil
	c.ProjectPreviews.BaseDomain = "previews.example.net"
	c.ProjectPreviews.PortMin = 20000
	c.ProjectPreviews.PortMax = 20031
	return c
}

func TestManagedPreviewConfiguration(t *testing.T) {
	if err := managedPreviewConfig().validateProjectPreviews(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"domain missing":   func(c *Config) { c.ProjectPreviews.BaseDomain = "" },
		"range missing":    func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 0, 0 },
		"privileged port":  func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 1023, 1024 },
		"large pool":       func(c *Config) { c.ProjectPreviews.PortMax = c.ProjectPreviews.PortMin + 1024 },
		"reversed pool":    func(c *Config) { c.ProjectPreviews.PortMax = c.ProjectPreviews.PortMin - 1 },
		"port overflow":    func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 65535, 65536 },
		"Deck listener":    func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 8837, 8838 },
		"gateway listener": func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 8840, 8841 },
		"static port": func(c *Config) {
			c.ProjectPreviews.Targets = []PreviewTarget{{ProjectID: "a", Origin: "https://a.example.org", Upstream: "[::1]:20001"}}
		},
		"same site": func(c *Config) { c.ProjectPreviews.BaseDomain = "preview.example.com" },
		"other Deck site": func(c *Config) {
			c.Server.AllowedOrigins = append(c.Server.AllowedOrigins, "https://other.example.net")
		},
		"HTTP Deck site": func(c *Config) { c.Server.AllowedOrigins = append(c.Server.AllowedOrigins, "http://other.example.net") },
		"uppercase":      func(c *Config) { c.ProjectPreviews.BaseDomain = "Preview.example.net" },
		"IP":             func(c *Config) { c.ProjectPreviews.BaseDomain = "127.0.0.1" },
		"wildcard":       func(c *Config) { c.ProjectPreviews.BaseDomain = "*.example.net" },
		"port":           func(c *Config) { c.ProjectPreviews.BaseDomain = "preview.example.net:8443" },
		"path":           func(c *Config) { c.ProjectPreviews.BaseDomain = "preview.example.net/" },
		"long generated host": func(c *Config) {
			c.ProjectPreviews.BaseDomain = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 35) + ".example.net"
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := managedPreviewConfig()
			change(c)
			if c.validateProjectPreviews() == nil {
				t.Fatal("invalid managed configuration accepted")
			}
		})
	}
	c := managedPreviewConfig()
	c.ProjectPreviews.Targets = previewConfig().ProjectPreviews.Targets
	if err := c.validateProjectPreviews(); err != nil {
		t.Fatalf("coexisting static and managed targets: %v", err)
	}
}

func TestManagedPreviewEnvironment(t *testing.T) {
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_BASE_DOMAIN", "previews.example.net")
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_HOSTNAME_PREFIX", "main-p-")
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_PORT_MIN", "20000")
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_PORT_MAX", "20031")
	var c Config
	if err := applyEnv(&c); err != nil {
		t.Fatal(err)
	}
	if p := c.ProjectPreviews; p.BaseDomain != "previews.example.net" || p.HostnamePrefix != "main-p-" || p.PortMin != 20000 || p.PortMax != 20031 {
		t.Fatalf("environment not applied: %+v", p)
	}
}

func TestManagedPreviewHostnamePrefix(t *testing.T) {
	for _, prefix := range []string{"", "p-", "main-p-", "1", strings.Repeat("a", 43)} {
		c := managedPreviewConfig()
		c.ProjectPreviews.HostnamePrefix = prefix
		if err := c.validateProjectPreviews(); err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
	}
	for _, prefix := range []string{"-a", "Main-", "a.b-", "a_", "a/", "a@", "a:", "a?", "a#", "a ", "é", strings.Repeat("a", 44)} {
		c := managedPreviewConfig()
		c.ProjectPreviews.HostnamePrefix = prefix
		if err := c.validateProjectPreviews(); err == nil {
			t.Fatalf("invalid prefix %q accepted", prefix)
		}
	}
	c := &Config{ProjectPreviews: ProjectPreviews{HostnamePrefix: "main-p-"}}
	if err := c.validateProjectPreviews(); err == nil {
		t.Fatal("prefix alone silently disabled previews")
	}
	c = managedPreviewConfig()
	c.ProjectPreviews.BaseDomain = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 20) + ".example.net"
	if err := c.validateProjectPreviews(); err != nil {
		t.Fatalf("default prefix fits: %v", err)
	}
	c.ProjectPreviews.HostnamePrefix = strings.Repeat("a", 43)
	if err := c.validateProjectPreviews(); err == nil {
		t.Fatal("prefix overflowed total DNS name length")
	}
}

func TestProjectPreviewConfigurationBoundary(t *testing.T) {
	if err := previewConfig().validateProjectPreviews(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"authentication disabled":    func(c *Config) { c.Auth.Mode = "none" },
		"trust not acknowledged":     func(c *Config) { c.ProjectPreviews.Unconfined = false },
		"public listener":            func(c *Config) { c.ProjectPreviews.Bind = "0.0.0.0:8840" },
		"control listener collision": func(c *Config) { c.ProjectPreviews.Bind = "127.0.0.1:8837" },
		"untrusted deck origin":      func(c *Config) { c.ProjectPreviews.DeckOrigin = "https://other.example.com" },
		"same site":                  func(c *Config) { c.ProjectPreviews.Targets[0].Origin = "https://preview.example.com" },
		"same site co.uk": func(c *Config) {
			c.ProjectPreviews.DeckOrigin = "https://deck.example.co.uk"
			c.Server.AllowedOrigins = []string{c.ProjectPreviews.DeckOrigin}
			c.ProjectPreviews.Targets[0].Origin = "https://preview.example.co.uk"
		},
		"Deck API upstream":             func(c *Config) { c.ProjectPreviews.Targets[0].Upstream = "127.0.0.1:8837" },
		"gateway upstream":              func(c *Config) { c.ProjectPreviews.Targets[0].Upstream = "127.0.0.1:8840" },
		"hostname upstream":             func(c *Config) { c.ProjectPreviews.Targets[0].Upstream = "localhost:5173" },
		"metadata upstream":             func(c *Config) { c.ProjectPreviews.Targets[0].Upstream = "169.254.169.254:80" },
		"duplicate project":             func(c *Config) { c.ProjectPreviews.Targets[1].ProjectID = "one" },
		"duplicate host different port": func(c *Config) { c.ProjectPreviews.Targets[1].Origin = "https://one.example.net:8443" },
		"duplicate endpoint":            func(c *Config) { c.ProjectPreviews.Targets[1].Upstream = "127.0.0.1:05173" },
		"HTTP origin":                   func(c *Config) { c.ProjectPreviews.Targets[0].Origin = "http://one.example.net" },
		"URL upstream":                  func(c *Config) { c.ProjectPreviews.Targets[0].Upstream = "http://127.0.0.1:5173" },
	} {
		t.Run(name, func(t *testing.T) {
			c := previewConfig()
			change(c)
			if c.validateProjectPreviews() == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestProjectPreviewEnvironmentTargets(t *testing.T) {
	var targets []PreviewTarget
	if err := decodePreviewTargets(`[{"project_id":"p","origin":"https://p.example.net","upstream":"127.0.0.1:5173"}]`, &targets); err != nil || len(targets) != 1 {
		t.Fatalf("decode: %v %v", targets, err)
	}
	for _, input := range []string{`[{"url":"http://internal"}]`, `[] {}`, `not json`} {
		if decodePreviewTargets(input, &targets) == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestPreviewMetadataSocketConfig(t *testing.T) {
	c := managedPreviewConfig()
	c.ProjectPreviews.MetadataSocket = "/private/preview.sock"
	if err := c.validateProjectPreviews(); err != nil {
		t.Fatal(err)
	}
	c.ProjectPreviews.MetadataSocket = "relative.sock"
	if err := c.validateProjectPreviews(); err == nil {
		t.Fatal("relative socket accepted")
	}
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET", "/private/alternate.sock")
	if err := applyEnv(c); err != nil {
		t.Fatal(err)
	}
	if c.ProjectPreviews.MetadataSocket != "/private/alternate.sock" {
		t.Fatal("socket override was not applied")
	}
	c = &Config{ProjectPreviews: ProjectPreviews{MetadataSocket: "/private/preview.sock"}}
	if err := c.validateProjectPreviews(); err == nil {
		t.Fatal("socket alone enabled unauthenticated previews")
	}
}

func loopbackPreviewConfig() *Config {
	c := Defaults()
	c.ProjectPreviews = ProjectPreviews{Loopback: true, Unconfined: true, PortMin: 15000, PortMax: 15007}
	return &c
}

func TestLoopbackPreviewConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"defaults", func(c *Config) {}, ""},
		{"IPv4 unspecified", func(c *Config) { c.ProjectPreviews.Bind = "0.0.0.0:8840" }, ""},
		{"IPv6 unspecified", func(c *Config) { c.ProjectPreviews.Bind = "[::]:8840" }, ""},
		{"single lowest port", func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 1024, 1024 }, ""},
		{"maximum pool", func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 65282, 65535 }, ""},
		{"custom listener port", func(c *Config) { c.ProjectPreviews.LoopbackPort = 8841; c.ProjectPreviews.Bind = "0.0.0.0:8841" }, ""},
		{"unconfined acknowledgement", func(c *Config) { c.ProjectPreviews.Unconfined = false }, "unconfined"},
		{"no authentication", func(c *Config) { c.Auth.Mode = "none" }, "authentication"},
		{"base domain", func(c *Config) { c.ProjectPreviews.BaseDomain = "apps.example.net" }, "exclusive"},
		{"hostname prefix", func(c *Config) { c.ProjectPreviews.HostnamePrefix = "p-" }, "exclusive"},
		{"static targets", func(c *Config) { c.ProjectPreviews.Targets = previewConfig().ProjectPreviews.Targets }, "exclusive"},
		{"deck origin", func(c *Config) { c.ProjectPreviews.DeckOrigin = "http://localhost:8837" }, "deck_origin must be empty"},
		{"no origins", func(c *Config) { c.Server.AllowedOrigins = nil }, "at least one"},
		{"network origin", func(c *Config) { c.Server.AllowedOrigins = append(c.Server.AllowedOrigins, "http://deck.example.com") }, "IPv4 loopback"},
		{"HTTPS origin", func(c *Config) { c.Server.AllowedOrigins = []string{"https://127.0.0.1:8837"} }, "loopback HTTP"},
		{"IPv6 origin", func(c *Config) { c.Server.AllowedOrigins = []string{"http://[::1]:8837"} }, "IPv4 loopback"},
		{"mapped IPv6 origin", func(c *Config) { c.Server.AllowedOrigins = []string{"http://[::ffff:127.0.0.1]:8837"} }, "IPv4 loopback"},
		{"localhost suffix", func(c *Config) { c.Server.AllowedOrigins = []string{"http://app.localhost:8837"} }, "IPv4 loopback"},
		{"origin path", func(c *Config) { c.Server.AllowedOrigins = []string{"http://localhost:8837/"} }, "exact"},
		{"origin credentials", func(c *Config) { c.Server.AllowedOrigins = []string{"http://user@localhost:8837"} }, "exact"},
		{"origin query", func(c *Config) { c.Server.AllowedOrigins = []string{"http://localhost:8837?"} }, "exact"},
		{"origin fragment", func(c *Config) { c.Server.AllowedOrigins = []string{"http://localhost:8837#"} }, "exact"},
		{"origin empty port", func(c *Config) { c.Server.AllowedOrigins = []string{"http://localhost:"} }, "port"},
		{"origin port overflow", func(c *Config) { c.Server.AllowedOrigins = []string{"http://localhost:65536"} }, "port"},
		{"pool missing", func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 0, 0 }, "between 1 and 254"},
		{"pool privileged", func(c *Config) { c.ProjectPreviews.PortMin = 1023 }, "between 1 and 254"},
		{"pool reversed", func(c *Config) { c.ProjectPreviews.PortMax = 14999 }, "between 1 and 254"},
		{"pool too large", func(c *Config) { c.ProjectPreviews.PortMax = 15254 }, "between 1 and 254"},
		{"pool overflow", func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 65535, 65536 }, "between 1 and 254"},
		{"pool Deck collision", func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 8837, 8837 }, "allocation ports"},
		{"pool preview collision", func(c *Config) { c.ProjectPreviews.PortMin, c.ProjectPreviews.PortMax = 8840, 8840 }, "allocation ports"},
		{"listener Deck collision", func(c *Config) { c.ProjectPreviews.LoopbackPort = 8837 }, "different port"},
		{"listener negative", func(c *Config) { c.ProjectPreviews.LoopbackPort = -1 }, "loopback_port"},
		{"listener overflow", func(c *Config) { c.ProjectPreviews.LoopbackPort = 65536 }, "loopback_port"},
		{"bind loopback", func(c *Config) { c.ProjectPreviews.Bind = "127.0.0.2:8840" }, "unspecified"},
		{"bind hostname", func(c *Config) { c.ProjectPreviews.Bind = "localhost:8840" }, "unspecified"},
		{"bind empty host", func(c *Config) { c.ProjectPreviews.Bind = ":8840" }, "unspecified"},
		{"bind network address", func(c *Config) { c.ProjectPreviews.Bind = "192.0.2.1:8840" }, "unspecified"},
		{"bind different port", func(c *Config) { c.ProjectPreviews.Bind = "0.0.0.0:8841" }, "loopback_port"},
		{"bind malformed", func(c *Config) { c.ProjectPreviews.Bind = "0.0.0.0" }, "unspecified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := loopbackPreviewConfig()
			tc.edit(c)
			err := c.validateProjectPreviews()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if c.ProjectPreviews.LoopbackPort == 0 {
					t.Fatal("default loopback port missing")
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoopbackPreviewEnvironment(t *testing.T) {
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_LOOPBACK", "true")
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_LOOPBACK_PORT", "8841")
	c := loopbackPreviewConfig()
	if err := applyEnv(c); err != nil {
		t.Fatal(err)
	}
	if !c.ProjectPreviews.Loopback || c.ProjectPreviews.LoopbackPort != 8841 {
		t.Fatalf("environment not applied: %+v", c.ProjectPreviews)
	}
	for key, value := range map[string]string{"NOISEDECK_PROJECT_PREVIEWS_LOOPBACK": "invalid", "NOISEDECK_PROJECT_PREVIEWS_LOOPBACK_PORT": "invalid"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if err := applyEnv(c); err == nil {
				t.Fatal("invalid environment accepted")
			}
		})
	}
}
