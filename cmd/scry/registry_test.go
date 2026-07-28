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
	}

	checks := configuredChecks(cfg)
	if len(checks) != 2 || checks[0].ID != "job" || checks[1].ID != "web" {
		t.Fatalf("registry order: %+v", checks)
	}
	if checks[0].Kind != model.KindPassive || checks[0].Period != 24*time.Hour || checks[0].Grace != 2*time.Hour || checks[0].HardenAfter != 5 {
		t.Fatalf("passive check: %+v", checks[0])
	}
	if checks[1].Kind != model.KindHTTP || checks[1].FailAfter != 4 {
		t.Fatalf("active check: %+v", checks[1])
	}
}
