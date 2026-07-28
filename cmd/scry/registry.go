package main

import (
	"github.com/michaelquigley/scry/internal/config"
	"github.com/michaelquigley/scry/internal/model"
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
