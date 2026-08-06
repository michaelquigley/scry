// Package config owns scry's fail-fast YAML configuration surface.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/michaelquigley/df/dd"
)

const (
	DefaultStatusListen = "0.0.0.0:8420"
	DefaultIngestListen = "127.0.0.1:8421"
)

var checkIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Config is scry's complete daemon configuration.
type Config struct {
	EstateName   string         `dd:"estate_name"`
	StatusListen string         `dd:"status_listen"`
	IngestListen string         `dd:"ingest_listen"`
	StateFile    string         `dd:"state_file"`
	Defaults     Defaults       `dd:"defaults"`
	Notifiers    NotifierConfig `dd:"notifiers"`
	Checks       []Check        `dd:"checks"`
}

// Defaults carries the timing and damping values inherited by checks.
type Defaults struct {
	Interval    time.Duration `dd:"interval"`
	Timeout     time.Duration `dd:"timeout"`
	FailAfter   int           `dd:"fail_after"`
	HardenAfter int           `dd:"harden_after"`
}

// NotifierConfig names every configured notification destination.
type NotifierConfig struct {
	Mattermost *MattermostConfig `dd:"mattermost"`
	SMTP       *SMTPConfig       `dd:"smtp"`
	Sendmail   *SendmailConfig   `dd:"sendmail"`
}

// SendmailConfig configures delivery through the host MTA's sendmail binary.
type SendmailConfig struct {
	Path string   `dd:"path"`
	From string   `dd:"from"`
	To   []string `dd:"to"`
}

// MattermostConfig configures bot-account posting to one channel.
type MattermostConfig struct {
	URL       string `dd:"url"`
	ChannelID string `dd:"channel_id"`
	TokenEnv  string `dd:"token_env"`
	Token     string `dd:"token"`
}

// SMTPConfig configures delivery through a house SMTP relay.
type SMTPConfig struct {
	Host string   `dd:"host"`
	Port int      `dd:"port"`
	From string   `dd:"from"`
	To   []string `dd:"to"`
}

// Check is one hand-curated monitored entity. exactly one strategy block is set.
//
// override pointers preserve the difference between omission and an authored
// zero: omission inherits Defaults, while a zero value is invalid config.
type Check struct {
	ID          string         `dd:"id"`
	Name        string         `dd:"name"`
	Passive     *PassiveConfig `dd:"passive"`
	HTTP        *HTTPConfig    `dd:"http"`
	TCP         *TCPConfig     `dd:"tcp"`
	Interval    *time.Duration `dd:"interval"`
	Timeout     *time.Duration `dd:"timeout"`
	FailAfter   *int           `dd:"fail_after"`
	HardenAfter *int           `dd:"harden_after"`
}

// PassiveConfig describes an expected report window.
type PassiveConfig struct {
	Period time.Duration `dd:"period"`
	Grace  time.Duration `dd:"grace"`
	Token  string        `dd:"token"`
}

// HTTPConfig describes an HTTP status probe.
type HTTPConfig struct {
	URL      string `dd:"url"`
	Expect   []int  `dd:"expect"`
	Insecure bool   `dd:"insecure"`
}

// TCPConfig describes a TCP dial probe.
type TCPConfig struct {
	Address string `dd:"address"`
}

// NewConfig returns the compiled bottom layer of the config cascade.
func NewConfig() *Config {
	return &Config{
		EstateName:   "scry",
		StatusListen: DefaultStatusListen,
		IngestListen: DefaultIngestListen,
		StateFile:    defaultStatePath(),
		Defaults: Defaults{
			Interval:    time.Minute,
			Timeout:     10 * time.Second,
			FailAfter:   3,
			HardenAfter: 3,
		},
	}
}

// Load resolves config by cascade, from lowest to highest priority:
//
//	compiled defaults -> ~/.config/scry/config.yaml -> ./scry.yaml -> --config flag
func Load(configPath string) (*Config, error) {
	cfg := NewConfig()
	if err := mergeIfExists(cfg, globalConfigPath()); err != nil {
		return nil, err
	}
	if err := mergeIfExists(cfg, "./scry.yaml"); err != nil {
		return nil, err
	}
	if configPath != "" {
		if err := dd.MergeYAMLFile(cfg, configPath); err != nil {
			return nil, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// Validate checks every invariant decidable before the daemon starts.
func (cfg *Config) Validate() error {
	if err := validateListen("status_listen", cfg.StatusListen, false); err != nil {
		return err
	}
	if err := validateListen("ingest_listen", cfg.IngestListen, true); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.EstateName) == "" {
		return fmt.Errorf("estate_name is required")
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		return fmt.Errorf("state_file is required")
	}
	if cfg.Defaults.Interval <= 0 {
		return fmt.Errorf("defaults.interval must be positive")
	}
	if cfg.Defaults.Timeout <= 0 {
		return fmt.Errorf("defaults.timeout must be positive")
	}
	if cfg.Defaults.FailAfter < 2 {
		return fmt.Errorf("defaults.fail_after must be at least 2")
	}
	if cfg.Defaults.HardenAfter < 1 {
		return fmt.Errorf("defaults.harden_after must be at least 1")
	}
	if err := cfg.Notifiers.validate(); err != nil {
		return err
	}

	ids := make(map[string]struct{}, len(cfg.Checks))
	tokens := make(map[string]string)
	for i := range cfg.Checks {
		check := &cfg.Checks[i]
		if err := check.validate(cfg.Defaults); err != nil {
			return fmt.Errorf("checks[%d]: %w", i, err)
		}
		if _, found := ids[check.ID]; found {
			return fmt.Errorf("checks[%d]: duplicate id %q", i, check.ID)
		}
		ids[check.ID] = struct{}{}
		if check.Passive != nil {
			if other, found := tokens[check.Passive.Token]; found {
				return fmt.Errorf("checks[%d]: passive token is already used by check %q", i, other)
			}
			tokens[check.Passive.Token] = check.ID
		}
	}
	return nil
}

// Kind returns the configured strategy name. it is empty before validation
// when the check has zero or multiple strategy blocks.
func (check *Check) Kind() string {
	switch {
	case check.Passive != nil && check.HTTP == nil && check.TCP == nil:
		return "passive"
	case check.Passive == nil && check.HTTP != nil && check.TCP == nil:
		return "http"
	case check.Passive == nil && check.HTTP == nil && check.TCP != nil:
		return "tcp"
	default:
		return ""
	}
}

// EffectiveInterval returns the check override or the inherited default.
func (check *Check) EffectiveInterval(defaults Defaults) time.Duration {
	if check.Interval != nil {
		return *check.Interval
	}
	return defaults.Interval
}

// EffectiveTimeout returns the check override or the inherited default.
func (check *Check) EffectiveTimeout(defaults Defaults) time.Duration {
	if check.Timeout != nil {
		return *check.Timeout
	}
	return defaults.Timeout
}

// EffectiveFailAfter returns the check override or the inherited default.
func (check *Check) EffectiveFailAfter(defaults Defaults) int {
	if check.FailAfter != nil {
		return *check.FailAfter
	}
	return defaults.FailAfter
}

// EffectiveHardenAfter returns the check override or the inherited default.
func (check *Check) EffectiveHardenAfter(defaults Defaults) int {
	if check.HardenAfter != nil {
		return *check.HardenAfter
	}
	return defaults.HardenAfter
}

func (check *Check) validate(defaults Defaults) error {
	if !checkIDPattern.MatchString(check.ID) {
		return fmt.Errorf("id %q must be a lowercase slug containing only letters, digits, and single hyphens", check.ID)
	}
	if strings.TrimSpace(check.Name) == "" {
		return fmt.Errorf("check %q: name is required", check.ID)
	}

	strategyCount := 0
	for _, configured := range []bool{check.Passive != nil, check.HTTP != nil, check.TCP != nil} {
		if configured {
			strategyCount++
		}
	}
	if strategyCount != 1 {
		return fmt.Errorf("check %q: exactly one of passive, http, or tcp is required", check.ID)
	}

	if check.Interval != nil && *check.Interval <= 0 {
		return fmt.Errorf("check %q: interval must be positive", check.ID)
	}
	if check.Timeout != nil && *check.Timeout <= 0 {
		return fmt.Errorf("check %q: timeout must be positive", check.ID)
	}
	if check.FailAfter != nil && *check.FailAfter < 2 {
		return fmt.Errorf("check %q: fail_after must be at least 2", check.ID)
	}
	if check.HardenAfter != nil && *check.HardenAfter < 1 {
		return fmt.Errorf("check %q: harden_after must be at least 1", check.ID)
	}

	switch {
	case check.Passive != nil:
		if check.Passive.Period <= 0 {
			return fmt.Errorf("check %q: passive.period must be positive", check.ID)
		}
		if check.Passive.Grace <= 0 {
			return fmt.Errorf("check %q: passive.grace must be positive", check.ID)
		}
		if check.Passive.Token == "" {
			return fmt.Errorf("check %q: passive.token is required", check.ID)
		}
		if check.EffectiveHardenAfter(defaults) < 1 {
			return fmt.Errorf("check %q: effective harden_after must be at least 1", check.ID)
		}
	case check.HTTP != nil:
		if err := validateHTTP(check.ID, check.HTTP); err != nil {
			return err
		}
		if err := check.validateActiveThresholds(defaults); err != nil {
			return err
		}
	case check.TCP != nil:
		if err := validateTargetAddress(check.ID, check.TCP.Address); err != nil {
			return err
		}
		if err := check.validateActiveThresholds(defaults); err != nil {
			return err
		}
	}
	return nil
}

func (check *Check) validateActiveThresholds(defaults Defaults) error {
	if check.EffectiveInterval(defaults) <= 0 {
		return fmt.Errorf("check %q: effective interval must be positive", check.ID)
	}
	if check.EffectiveTimeout(defaults) <= 0 {
		return fmt.Errorf("check %q: effective timeout must be positive", check.ID)
	}
	if check.EffectiveFailAfter(defaults) < 2 {
		return fmt.Errorf("check %q: effective fail_after must be at least 2", check.ID)
	}
	return nil
}

func (cfg NotifierConfig) validate() error {
	if cfg.Mattermost != nil {
		parsed, err := url.Parse(cfg.Mattermost.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("notifiers.mattermost.url must be an http or https URL")
		}
		if strings.TrimSpace(cfg.Mattermost.ChannelID) == "" {
			return fmt.Errorf("notifiers.mattermost.channel_id is required")
		}
		if cfg.Mattermost.resolvedToken() == "" {
			return fmt.Errorf("notifiers.mattermost token is required through token_env or token")
		}
	}
	if cfg.SMTP != nil {
		if strings.TrimSpace(cfg.SMTP.Host) == "" {
			return fmt.Errorf("notifiers.smtp.host is required")
		}
		if cfg.SMTP.Port < 1 || cfg.SMTP.Port > 65535 {
			return fmt.Errorf("notifiers.smtp.port must be between 1 and 65535")
		}
		if strings.TrimSpace(cfg.SMTP.From) == "" {
			return fmt.Errorf("notifiers.smtp.from is required")
		}
		if _, err := mail.ParseAddress(cfg.SMTP.From); err != nil {
			return fmt.Errorf("notifiers.smtp.from must be a valid email address")
		}
		if len(cfg.SMTP.To) == 0 {
			return fmt.Errorf("notifiers.smtp.to must contain at least one recipient")
		}
		for i, recipient := range cfg.SMTP.To {
			if strings.TrimSpace(recipient) == "" {
				return fmt.Errorf("notifiers.smtp.to[%d] is empty", i)
			}
			if _, err := mail.ParseAddress(recipient); err != nil {
				return fmt.Errorf("notifiers.smtp.to[%d] must be a valid email address", i)
			}
		}
	}
	if cfg.Sendmail != nil {
		if _, err := mail.ParseAddress(cfg.Sendmail.From); err != nil {
			return fmt.Errorf("notifiers.sendmail.from must be a valid email address")
		}
		if len(cfg.Sendmail.To) == 0 {
			return fmt.Errorf("notifiers.sendmail.to must contain at least one recipient")
		}
		for i, recipient := range cfg.Sendmail.To {
			if _, err := mail.ParseAddress(recipient); err != nil {
				return fmt.Errorf("notifiers.sendmail.to[%d] must be a valid email address", i)
			}
		}
	}
	return nil
}

func (cfg MattermostConfig) resolvedToken() string {
	if cfg.TokenEnv != "" {
		if token := os.Getenv(cfg.TokenEnv); token != "" {
			return token
		}
	}
	return cfg.Token
}

func validateHTTP(checkID string, cfg *HTTPConfig) error {
	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("check %q: http.url must be an http or https URL", checkID)
	}
	for _, code := range cfg.Expect {
		if code < 100 || code > 599 {
			return fmt.Errorf("check %q: http.expect code %d must be between 100 and 599", checkID, code)
		}
	}
	return nil
}

func validateListen(name, address string, loopbackOnly bool) error {
	host, _, err := splitNumericAddress(address)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if !loopbackOnly {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must bind a loopback address", name)
	}
	return nil
}

func validateTargetAddress(checkID, address string) error {
	host, _, err := splitNumericAddress(address)
	if err != nil {
		return fmt.Errorf("check %q: tcp.address: %w", checkID, err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("check %q: tcp.address host is required", checkID)
	}
	return nil
}

func splitNumericAddress(address string) (string, int, error) {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("%q must use host:port syntax: %w", address, err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("%q must use a numeric port between 1 and 65535", address)
	}
	return host, port, nil
}

func mergeIfExists(cfg *Config, path string) error {
	err := dd.MergeYAMLFile(cfg, path)
	if err != nil {
		var fileErr *dd.FileError
		if errors.As(err, &fileErr) && fileErr.IsNotFound() {
			return nil
		}
		return err
	}
	return nil
}

func globalConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "scry", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "scry", "config.yaml")
	}
	return filepath.Join(home, ".config", "scry", "config.yaml")
}

func defaultStatePath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "scry", "state.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "state", "scry", "state.json")
	}
	return filepath.Join(home, ".local", "state", "scry", "state.json")
}
