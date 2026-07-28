package main

import (
	"github.com/michaelquigley/scry/internal/config"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/strategy"
)

// configuredChecks maps the validated transport-shaped config into core checks.
func configuredChecks(cfg *config.Config) []model.Check {
	checks := make([]model.Check, len(cfg.Checks))
	for i, configured := range cfg.Checks {
		check := model.Check{
			ID:   configured.ID,
			Name: configured.Name,
			Kind: model.Kind(configured.Kind()),
		}
		switch check.Kind {
		case model.KindPassive:
			check.Period = configured.Passive.Period
			check.Grace = configured.Passive.Grace
			check.HardenAfter = configured.EffectiveHardenAfter(cfg.Defaults)
		case model.KindHTTP, model.KindTCP:
			check.FailAfter = configured.EffectiveFailAfter(cfg.Defaults)
		}
		checks[i] = check
	}
	return checks
}

// configuredActiveChecks builds the active scheduling registry.
func configuredActiveChecks(cfg *config.Config) []engine.ActiveCheck {
	checks := make([]engine.ActiveCheck, 0, len(cfg.Checks))
	for i := range cfg.Checks {
		configured := &cfg.Checks[i]
		var evaluator strategy.CheckStrategy
		switch {
		case configured.HTTP != nil:
			evaluator = strategy.NewHTTP(
				configured.HTTP.URL,
				configured.HTTP.Expect,
				configured.HTTP.Insecure,
			)
		case configured.TCP != nil:
			evaluator = strategy.NewTCP(configured.TCP.Address)
		default:
			continue
		}
		checks = append(checks, engine.ActiveCheck{
			ID:       configured.ID,
			Interval: configured.EffectiveInterval(cfg.Defaults),
			Timeout:  configured.EffectiveTimeout(cfg.Defaults),
			Strategy: evaluator,
		})
	}
	return checks
}
