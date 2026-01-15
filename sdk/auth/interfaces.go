package auth

import (
	"context"
	"errors"
	"time"

	"github.com/shariqriazz/modelgate/internal/config"
	coreauth "github.com/shariqriazz/modelgate/sdk/cliproxy/auth"
)

var ErrRefreshNotSupported = errors.New("modelgate auth: refresh not supported")

// LoginOptions captures generic knobs shared across authenticators.
// Provider-specific logic can inspect Metadata for extra parameters.
type LoginOptions struct {
	NoBrowser    bool
	ProjectID    string
	CallbackPort int
	Metadata     map[string]string
	Prompt       func(prompt string) (string, error)
}

// Authenticator manages login and optional refresh flows for a provider.
type Authenticator interface {
	Provider() string
	Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error)
	RefreshLead() *time.Duration
}
