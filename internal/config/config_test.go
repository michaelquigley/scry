package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func duration(value time.Duration) *time.Duration {
	return &value
}

func integer(value int) *int {
	return &value
}

func validConfig() *Config {
	return &Config{
		EstateName:   "test estate",
		StatusListen: DefaultStatusListen,
		IngestListen: DefaultIngestListen,
		StateFile:    "/tmp/scry-state.json",
		HistoryDir:   "/tmp/scry-history",
		Defaults: Defaults{
			Interval:    time.Minute,
			Timeout:     10 * time.Second,
			FailAfter:   3,
			HardenAfter: 3,
		},
		Checks: []Check{{
			ID:   "service-one",
			Name: "service one",
			HTTP: &HTTPConfig{URL: "https://service.example.com/"},
		}},
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewConfig(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	cfg := NewConfig()
	if cfg.StatusListen != "0.0.0.0:8420" {
		t.Errorf("status listen: %q", cfg.StatusListen)
	}
	if cfg.IngestListen != "127.0.0.1:8421" {
		t.Errorf("ingest listen: %q", cfg.IngestListen)
	}
	if want := filepath.Join(stateHome, "scry", "state.json"); cfg.StateFile != want {
		t.Errorf("state file: %q, want %q", cfg.StateFile, want)
	}
	if want := filepath.Join(stateHome, "scry", "history"); cfg.HistoryDir != want {
		t.Errorf("history dir: %q, want %q", cfg.HistoryDir, want)
	}
	if cfg.Defaults.Interval != time.Minute || cfg.Defaults.Timeout != 10*time.Second {
		t.Errorf("timing defaults: %+v", cfg.Defaults)
	}
	if cfg.Defaults.FailAfter != 3 || cfg.Defaults.HardenAfter != 3 {
		t.Errorf("damping defaults: %+v", cfg.Defaults)
	}
}

func TestLoadCascadeAndDurationBinding(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	configHome := filepath.Join(root, "config-home")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state-home"))

	writeConfig(t, filepath.Join(configHome, "scry", "config.yaml"), `
status_listen: "127.0.0.1:9000"
defaults:
  interval: 2m
  timeout: 20s
  fail_after: 4
  harden_after: 5
`)
	writeConfig(t, filepath.Join(root, "scry.yaml"), `
status_listen: "127.0.0.1:9001"
checks:
  - id: web
    name: "web"
    http:
      url: "https://web.example.com/"
      address: "192.0.2.10:8443"
    interval: 30s
`)
	explicitPath := filepath.Join(root, "explicit.yaml")
	writeConfig(t, explicitPath, `
status_listen: "127.0.0.1:9002"
ingest_listen: "localhost:9003"
history_dir: "/var/lib/scry/history"
`)

	cfg, err := Load(explicitPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatusListen != "127.0.0.1:9002" {
		t.Errorf("status cascade: %q", cfg.StatusListen)
	}
	if cfg.IngestListen != "localhost:9003" {
		t.Errorf("ingest cascade: %q", cfg.IngestListen)
	}
	if cfg.HistoryDir != "/var/lib/scry/history" {
		t.Errorf("history dir cascade: %q", cfg.HistoryDir)
	}
	if cfg.Defaults.Interval != 2*time.Minute || cfg.Defaults.Timeout != 20*time.Second {
		t.Errorf("duration binding: %+v", cfg.Defaults)
	}
	if len(cfg.Checks) != 1 || cfg.Checks[0].EffectiveInterval(cfg.Defaults) != 30*time.Second {
		t.Fatalf("check override: %+v", cfg.Checks)
	}
	if cfg.Checks[0].HTTP.Address != "192.0.2.10:8443" {
		t.Errorf("http address binding: %+v", cfg.Checks[0].HTTP)
	}
	if cfg.Checks[0].EffectiveTimeout(cfg.Defaults) != 20*time.Second {
		t.Errorf("inherited timeout: %s", cfg.Checks[0].EffectiveTimeout(cfg.Defaults))
	}
}

func TestLoadMissingExplicitFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config-home"))

	_, err := Load(filepath.Join(root, "missing.yaml"))
	if err == nil {
		t.Fatal("expected an explicit missing file to fail")
	}
}

func TestLoadRejectsExplicitZeroOverride(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config-home"))

	path := filepath.Join(root, "explicit.yaml")
	writeConfig(t, path, `
checks:
  - id: web
    name: "web"
    http:
      url: "https://web.example.com/"
    interval: 0s
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "interval must be positive") {
		t.Fatalf("error: %v", err)
	}
}

func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"empty status listen", func(c *Config) { c.StatusListen = "" }, "status_listen"},
		{"nonnumeric status port", func(c *Config) { c.StatusListen = "127.0.0.1:http" }, "numeric port"},
		{"public ingest", func(c *Config) { c.IngestListen = "0.0.0.0:8421" }, "loopback"},
		{"empty estate name", func(c *Config) { c.EstateName = "  " }, "estate_name"},
		{"empty state file", func(c *Config) { c.StateFile = "" }, "state_file"},
		{"empty history dir", func(c *Config) { c.HistoryDir = "" }, "history_dir"},
		{"zero default interval", func(c *Config) { c.Defaults.Interval = 0 }, "defaults.interval"},
		{"zero default timeout", func(c *Config) { c.Defaults.Timeout = 0 }, "defaults.timeout"},
		{"low default fail after", func(c *Config) { c.Defaults.FailAfter = 1 }, "defaults.fail_after"},
		{"low default harden after", func(c *Config) { c.Defaults.HardenAfter = 0 }, "defaults.harden_after"},
		{"empty id", func(c *Config) { c.Checks[0].ID = "" }, "lowercase slug"},
		{"uppercase id", func(c *Config) { c.Checks[0].ID = "Service" }, "lowercase slug"},
		{"leading hyphen id", func(c *Config) { c.Checks[0].ID = "-service" }, "lowercase slug"},
		{"empty name", func(c *Config) { c.Checks[0].Name = "" }, "name is required"},
		{"zero strategies", func(c *Config) { c.Checks[0].HTTP = nil }, "exactly one"},
		{"multiple strategies", func(c *Config) { c.Checks[0].TCP = &TCPConfig{Address: "host:80"} }, "exactly one"},
		{"zero interval override", func(c *Config) { c.Checks[0].Interval = duration(0) }, "interval must be positive"},
		{"zero timeout override", func(c *Config) { c.Checks[0].Timeout = duration(0) }, "timeout must be positive"},
		{"low fail override", func(c *Config) { c.Checks[0].FailAfter = integer(1) }, "fail_after must be at least 2"},
		{"low harden override", func(c *Config) { c.Checks[0].HardenAfter = integer(0) }, "harden_after must be at least 1"},
		{"malformed http url", func(c *Config) { c.Checks[0].HTTP.URL = "service.example.com" }, "http.url"},
		{"unsupported http scheme", func(c *Config) { c.Checks[0].HTTP.URL = "ftp://service.example.com" }, "http.url"},
		{"low expect code", func(c *Config) { c.Checks[0].HTTP.Expect = []int{99} }, "between 100 and 599"},
		{"high expect code", func(c *Config) { c.Checks[0].HTTP.Expect = []int{600} }, "between 100 and 599"},
		{"malformed http address", func(c *Config) { c.Checks[0].HTTP.Address = "service" }, "host:port"},
		{"nonnumeric http port", func(c *Config) { c.Checks[0].HTTP.Address = "service:web" }, "numeric port"},
		{"empty http address host", func(c *Config) { c.Checks[0].HTTP.Address = ":80" }, "http.address"},
		{"malformed tcp address", func(c *Config) {
			c.Checks[0].HTTP = nil
			c.Checks[0].TCP = &TCPConfig{Address: "host"}
		}, "host:port"},
		{"nonnumeric tcp port", func(c *Config) {
			c.Checks[0].HTTP = nil
			c.Checks[0].TCP = &TCPConfig{Address: "host:postgres"}
		}, "numeric port"},
		{"empty passive period", func(c *Config) {
			c.Checks[0].HTTP = nil
			c.Checks[0].Passive = &PassiveConfig{Grace: time.Hour, Token: "token"}
		}, "passive.period"},
		{"empty passive grace", func(c *Config) {
			c.Checks[0].HTTP = nil
			c.Checks[0].Passive = &PassiveConfig{Period: time.Hour, Token: "token"}
		}, "passive.grace"},
		{"empty passive token", func(c *Config) {
			c.Checks[0].HTTP = nil
			c.Checks[0].Passive = &PassiveConfig{Period: time.Hour, Grace: time.Minute}
		}, "passive.token"},
		{"bad mattermost url", func(c *Config) {
			c.Notifiers.Mattermost = &MattermostConfig{URL: "mattermost.example.com", ChannelID: "channel", Token: "token"}
		}, "mattermost.url"},
		{"missing mattermost channel", func(c *Config) {
			c.Notifiers.Mattermost = &MattermostConfig{URL: "https://mattermost.example.com", Token: "token"}
		}, "channel_id"},
		{"bad smtp port", func(c *Config) {
			c.Notifiers.SMTP = &SMTPConfig{Host: "smtp", Port: 0, From: "scry@example.com", To: []string{"me@example.com"}}
		}, "smtp.port"},
		{"bad smtp from", func(c *Config) {
			c.Notifiers.SMTP = &SMTPConfig{Host: "smtp", Port: 25, From: "not an address", To: []string{"me@example.com"}}
		}, "smtp.from"},
		{"bad sendmail from", func(c *Config) {
			c.Notifiers.Sendmail = &SendmailConfig{From: "not an address", To: []string{"me@example.com"}}
		}, "sendmail.from"},
		{"empty sendmail recipients", func(c *Config) {
			c.Notifiers.Sendmail = &SendmailConfig{From: "scry@example.com"}
		}, "sendmail.to"},
		{"bad sendmail recipient", func(c *Config) {
			c.Notifiers.Sendmail = &SendmailConfig{From: "scry@example.com", To: []string{"not an address"}}
		}, "sendmail.to[0]"},
		{"bad smtp recipient", func(c *Config) {
			c.Notifiers.SMTP = &SMTPConfig{Host: "smtp", Port: 25, From: "scry@example.com", To: []string{"not an address"}}
		}, "smtp.to[0]"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.edit(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

func TestMattermostTokenResolution(t *testing.T) {
	const tokenEnv = "SCRY_TEST_MATTERMOST_TOKEN"
	tests := []struct {
		name      string
		envToken  string
		token     string
		want      string
		wantError bool
	}{
		{name: "environment wins over inline", envToken: "from-env", token: "inline", want: "from-env"},
		{name: "inline fallback", token: "inline", want: "inline"},
		{name: "both empty rejected", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(tokenEnv, test.envToken)
			cfg := validConfig()
			cfg.Notifiers.Mattermost = &MattermostConfig{
				URL:       "https://mattermost.example.com",
				ChannelID: "channel",
				TokenEnv:  tokenEnv,
				Token:     test.token,
			}
			err := cfg.Validate()
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "token is required") {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Notifiers.Mattermost.resolvedToken(); got != test.want {
				t.Fatalf("resolved token: %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateDuplicateIdentity(t *testing.T) {
	t.Run("check ids", func(t *testing.T) {
		cfg := validConfig()
		cfg.Checks = append(cfg.Checks, Check{
			ID:   cfg.Checks[0].ID,
			Name: "duplicate",
			TCP:  &TCPConfig{Address: "host:80"},
		})
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "duplicate id") {
			t.Fatalf("error: %v", err)
		}
	})

	t.Run("passive tokens", func(t *testing.T) {
		cfg := validConfig()
		cfg.Checks = []Check{
			{
				ID:      "job-one",
				Name:    "job one",
				Passive: &PassiveConfig{Period: time.Hour, Grace: time.Minute, Token: "shared"},
			},
			{
				ID:      "job-two",
				Name:    "job two",
				Passive: &PassiveConfig{Period: time.Hour, Grace: time.Minute, Token: "shared"},
			},
		}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "already used") {
			t.Fatalf("error: %v", err)
		}
	})
}

func TestValidStrategyKindsAndOverrides(t *testing.T) {
	cfg := validConfig()
	cfg.Checks = []Check{
		{
			ID:          "job",
			Name:        "job",
			Passive:     &PassiveConfig{Period: 24 * time.Hour, Grace: 2 * time.Hour, Token: "one"},
			HardenAfter: integer(4),
		},
		{
			ID:        "web",
			Name:      "web",
			HTTP:      &HTTPConfig{URL: "https://web.example.com/", Expect: []int{200, 301}},
			Interval:  duration(30 * time.Second),
			Timeout:   duration(5 * time.Second),
			FailAfter: integer(5),
		},
		{
			ID:   "database",
			Name: "database",
			TCP:  &TCPConfig{Address: "[::1]:5432"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"passive", "http", "tcp"} {
		if got := cfg.Checks[i].Kind(); got != want {
			t.Errorf("check %d kind: %q, want %q", i, got, want)
		}
	}
	if got := cfg.Checks[0].EffectiveHardenAfter(cfg.Defaults); got != 4 {
		t.Errorf("harden override: %d", got)
	}
	if got := cfg.Checks[1].EffectiveFailAfter(cfg.Defaults); got != 5 {
		t.Errorf("failure override: %d", got)
	}
	if got := cfg.Checks[2].EffectiveInterval(cfg.Defaults); got != time.Minute {
		t.Errorf("inherited interval: %s", got)
	}
}
