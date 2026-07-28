package strategy

import (
	"context"
	"net"

	"github.com/michaelquigley/scry/internal/model"
)

// TCP evaluates whether an address accepts a TCP connection.
type TCP struct {
	address string
	dialer  net.Dialer
}

// NewTCP returns a TCP strategy for address.
func NewTCP(address string) *TCP {
	return &TCP{address: address}
}

// Evaluate dials the configured address within ctx.
func (strategy *TCP) Evaluate(ctx context.Context) model.Result {
	connection, err := strategy.dialer.DialContext(ctx, "tcp", strategy.address)
	if err != nil {
		return model.Result{Status: model.StatusFailed, Detail: err.Error()}
	}
	_ = connection.Close()
	return model.Result{Status: model.StatusOK}
}
