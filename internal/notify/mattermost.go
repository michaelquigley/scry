package notify

import (
	"context"
	"fmt"

	harnessmattermost "github.com/michaelquigley/theharnessbody/mattermost"

	"github.com/michaelquigley/scry/internal/model"
)

// Mattermost delivers transitions through a bot account.
type Mattermost struct {
	channelID string
	client    *harnessmattermost.Client
}

// NewMattermost returns a posting-only adapter over the shared house client.
func NewMattermost(url, channelID, tokenEnv, token string) *Mattermost {
	return &Mattermost{
		channelID: channelID,
		client: harnessmattermost.NewClient(harnessmattermost.Config{
			URL:      url,
			Token:    token,
			TokenEnv: tokenEnv,
		}),
	}
}

// Notify posts the shared transition message.
func (notifier *Mattermost) Notify(ctx context.Context, transition model.Transition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := notifier.client.PostMessage(notifier.channelID, Message(transition)); err != nil {
		return fmt.Errorf("post mattermost message: %w", err)
	}
	return nil
}
