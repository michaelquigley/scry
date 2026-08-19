package main

import (
	"fmt"

	"github.com/michaelquigley/scry/internal/config"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/ingest"
	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/notify"
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

// configuredPassiveChecks builds the ingest authentication registry.
func configuredPassiveChecks(cfg *config.Config) []ingest.Check {
	checks := make([]ingest.Check, 0, len(cfg.Checks))
	for i := range cfg.Checks {
		configured := &cfg.Checks[i]
		if configured.Passive == nil {
			continue
		}
		checks = append(checks, ingest.Check{
			ID:    configured.ID,
			Token: configured.Passive.Token,
		})
	}
	return checks
}

// configuredNotifiers builds every configured delivery destination.
func configuredNotifiers(cfg *config.Config) ([]notify.Destination, error) {
	destinations := make([]notify.Destination, 0, 2)
	if cfg.Notifiers.Mattermost != nil {
		configured := cfg.Notifiers.Mattermost
		destinations = append(destinations, notify.Destination{
			Name: "mattermost",
			Notifier: notify.NewMattermost(
				configured.URL,
				configured.ChannelID,
				configured.TokenEnv,
				configured.Token,
			),
		})
	}
	if cfg.Notifiers.SMTP != nil {
		configured := cfg.Notifiers.SMTP
		notifier, err := notify.NewSMTP(
			configured.Host,
			configured.Port,
			configured.From,
			configured.To,
		)
		if err != nil {
			return nil, fmt.Errorf("configure smtp notifier: %w", err)
		}
		destinations = append(destinations, notify.Destination{
			Name:     "smtp",
			Notifier: notifier,
		})
	}
	if cfg.Notifiers.Sendmail != nil {
		configured := cfg.Notifiers.Sendmail
		notifier, err := notify.NewSendmail(
			configured.Path,
			configured.From,
			configured.To,
		)
		if err != nil {
			return nil, fmt.Errorf("configure sendmail notifier: %w", err)
		}
		destinations = append(destinations, notify.Destination{
			Name:     "sendmail",
			Notifier: notifier,
		})
	}
	return destinations, nil
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
				configured.HTTP.Address,
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
