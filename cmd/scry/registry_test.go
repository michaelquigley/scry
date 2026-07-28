package main

import (
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/config"
	"github.com/michaelquigley/scry/internal/model"
)

func TestConfiguredChecksPreserveRegistryOrderAndRules(t *testing.T) {
	failAfter := 4
	hardenAfter := 5
	interval := 2 * time.Minute
	timeout := 7 * time.Second
	cfg := config.NewConfig()
	cfg.Checks = []config.Check{
		{
			ID:          "job",
			Name:        "job",
			Passive:     &config.PassiveConfig{Period: 24 * time.Hour, Grace: 2 * time.Hour, Token: "token"},
			HardenAfter: &hardenAfter,
		},
		{
			ID:        "web",
			Name:      "web",
			HTTP:      &config.HTTPConfig{URL: "https://web.example.com/"},
			FailAfter: &failAfter,
		},
		{
			ID:       "ssh",
			Name:     "ssh",
			TCP:      &config.TCPConfig{Address: "127.0.0.1:22"},
			Interval: &interval,
			Timeout:  &timeout,
		},
	}

	checks := configuredChecks(cfg)
	if len(checks) != 3 || checks[0].ID != "job" || checks[1].ID != "web" || checks[2].ID != "ssh" {
		t.Fatalf("registry order: %+v", checks)
	}
	if checks[0].Kind != model.KindPassive || checks[0].Period != 24*time.Hour || checks[0].Grace != 2*time.Hour || checks[0].HardenAfter != 5 {
		t.Fatalf("passive check: %+v", checks[0])
	}
	if checks[1].Kind != model.KindHTTP || checks[1].FailAfter != 4 {
		t.Fatalf("active check: %+v", checks[1])
	}
	if checks[2].Kind != model.KindTCP || checks[2].FailAfter != cfg.Defaults.FailAfter {
		t.Fatalf("tcp check: %+v", checks[2])
	}

	active := configuredActiveChecks(cfg)
	if len(active) != 2 || active[0].ID != "web" || active[1].ID != "ssh" {
		t.Fatalf("active registry order: %+v", active)
	}
	if active[0].Interval != cfg.Defaults.Interval || active[0].Timeout != cfg.Defaults.Timeout {
		t.Fatalf("http scheduling defaults: %+v", active[0])
	}
	if active[1].Interval != interval || active[1].Timeout != timeout {
		t.Fatalf("tcp scheduling overrides: %+v", active[1])
	}
	if active[0].Strategy == nil || active[1].Strategy == nil {
		t.Fatalf("active strategies: %+v", active)
	}

	passive := configuredPassiveChecks(cfg)
	if len(passive) != 1 || passive[0].ID != "job" || passive[0].Token != "token" {
		t.Fatalf("passive registry: %+v", passive)
	}
}
