package auth

import (
	"time"

	modelgateauth "github.com/shariqriazz/modelgate/sdk/cliproxy/auth"
)

func init() {
	registerRefreshLead("codex", func() Authenticator { return NewCodexAuthenticator() })
	registerRefreshLead("qwen", func() Authenticator { return NewQwenAuthenticator() })
	registerRefreshLead("iflow", func() Authenticator { return NewIFlowAuthenticator() })
	registerRefreshLead("gemini", func() Authenticator { return NewGeminiAuthenticator() })
	registerRefreshLead("gemini-cli", func() Authenticator { return NewGeminiAuthenticator() })
	registerRefreshLead("antigravity", func() Authenticator { return NewAntigravityAuthenticator() })
	registerRefreshLead("github-copilot", func() Authenticator { return NewGitHubCopilotAuthenticator() })
}

func registerRefreshLead(provider string, factory func() Authenticator) {
	modelgateauth.RegisterRefreshLeadProvider(provider, func() *time.Duration {
		if factory == nil {
			return nil
		}
		auth := factory()
		if auth == nil {
			return nil
		}
		return auth.RefreshLead()
	})
}
