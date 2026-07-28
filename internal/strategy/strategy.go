// Package strategy contains active probes behind the CheckStrategy contract.
package strategy

import (
	"context"

	"github.com/michaelquigley/scry/internal/model"
)

// CheckStrategy evaluates one active check and always returns a result.
type CheckStrategy interface {
	Evaluate(context.Context) model.Result
}
