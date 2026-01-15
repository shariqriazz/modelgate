package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret = "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"
	antigravityCallbackPort = 51121
)

var antigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// AntigravityAuthenticator implements OAuth login for the antigravity provider.
type AntigravityAuthenticator struct{}

// NewAntigravityAuthenticator constructs a new authenticator instance.
func NewAntigravityAuthenticator() Authenticator { return &AntigravityAuthenticator{} }

// Provider returns the provider key for antigravity.
func (AntigravityAuthenticator) Provider() string { return "antigravity" }

// RefreshLead instructs the manager to refresh five minutes before expiry.
func (AntigravityAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

// Login launches a local OAuth flow to obtain antigravity tokens and persists them.
func (AntigravityAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := antigravityCallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	httpClient := util.SetProxy(&cfg.SDKConfig, &http.Client{})

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("antigravity: failed to generate state: %w", err)
	}

	srv, port, cbChan, errServer := startAntigravityCallbackServer(callbackPort)
	if errServer != nil {
		return nil, fmt.Errorf("antigravity: failed to start callback server: %w", errServer)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", port)
	authURL := buildAntigravityAuthURL(redirectURI, state)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for antigravity authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(port)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for antigravity authentication callback...")

	var cbRes callbackResult
	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	var manualPromptTimer *time.Timer
	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer = time.NewTimer(15 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}

waitForCallback:
	for {
		select {
		case res := <-cbChan:
			cbRes = res
			break waitForCallback
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case res := <-cbChan:
				cbRes = res
				break waitForCallback
			default:
			}
			input, errPrompt := opts.Prompt("Paste the antigravity callback URL (or press Enter to keep waiting): ")
			if errPrompt != nil {
				return nil, errPrompt
			}
			parsed, errParse := misc.ParseOAuthCallback(input)
			if errParse != nil {
				return nil, errParse
			}
			if parsed == nil {
				continue
			}
			cbRes = callbackResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("antigravity: authentication timed out")
		}
	}

	if cbRes.Error != "" {
		return nil, fmt.Errorf("antigravity: authentication failed: %s", cbRes.Error)
	}
	if cbRes.State != state {
		return nil, fmt.Errorf("antigravity: invalid state")
	}
	if cbRes.Code == "" {
		return nil, fmt.Errorf("antigravity: missing authorization code")
	}

	tokenResp, errToken := exchangeAntigravityCode(ctx, cbRes.Code, redirectURI, httpClient)
	if errToken != nil {
		return nil, fmt.Errorf("antigravity: token exchange failed: %w", errToken)
	}

	email := ""
	if tokenResp.AccessToken != "" {
		if info, errInfo := fetchAntigravityUserInfo(ctx, tokenResp.AccessToken, httpClient); errInfo == nil && strings.TrimSpace(info.Email) != "" {
			email = strings.TrimSpace(info.Email)
		}
	}

	// Fetch project ID and tier via loadCodeAssist (same approach as Gemini CLI)
	projectID := ""
	tier := ""
	if tokenResp.AccessToken != "" {
		discovery, errProject := fetchAntigravityProjectAndTier(ctx, tokenResp.AccessToken, httpClient)
		if errProject != nil {
			log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
		} else {
			projectID = discovery.ProjectID
			tier = discovery.Tier
			log.Infof("antigravity: obtained project ID %s (tier: %s)", projectID, tier)
		}
	}

	now := time.Now()
	metadata := map[string]any{
		"type":          "antigravity",
		"access_token":  tokenResp.AccessToken,
		"refresh_token": tokenResp.RefreshToken,
		"expires_in":    tokenResp.ExpiresIn,
		"timestamp":     now.UnixMilli(),
		"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
	}
	if email != "" {
		metadata["email"] = email
	}
	if projectID != "" {
		metadata["project_id"] = projectID
	}
	if tier != "" {
		metadata["tier"] = tier
	}

	fileName := sanitizeAntigravityFileName(email)
	label := email
	if label == "" {
		label = "antigravity"
	}

	fmt.Println("Antigravity authentication successful")
	if projectID != "" {
		tierInfo := ""
		if tier != "" {
			tierInfo = fmt.Sprintf(" (tier: %s)", tier)
		}
		fmt.Printf("Using GCP project: %s%s\n", projectID, tierInfo)
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "antigravity",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}, nil
}

type callbackResult struct {
	Code  string
	Error string
	State string
}

func startAntigravityCallbackServer(port int) (*http.Server, int, <-chan callbackResult, error) {
	if port <= 0 {
		port = antigravityCallbackPort
	}
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, 0, nil, err
	}
	port = listener.Addr().(*net.TCPAddr).Port
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := callbackResult{
			Code:  strings.TrimSpace(q.Get("code")),
			Error: strings.TrimSpace(q.Get("error")),
			State: strings.TrimSpace(q.Get("state")),
		}
		resultCh <- res
		if res.Code != "" && res.Error == "" {
			_, _ = w.Write([]byte("<h1>Login successful</h1><p>You can close this window.</p>"))
		} else {
			_, _ = w.Write([]byte("<h1>Login failed</h1><p>Please check the CLI output.</p>"))
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("antigravity callback server error: %v", errServe)
		}
	}()

	return srv, port, resultCh, nil
}

type antigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func exchangeAntigravityCode(ctx context.Context, code, redirectURI string, httpClient *http.Client) (*antigravityTokenResponse, error) {
	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", antigravityClientID)
	data.Set("client_secret", antigravityClientSecret)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity token exchange: close body error: %v", errClose)
		}
	}()

	var token antigravityTokenResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&token); errDecode != nil {
		return nil, errDecode
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("oauth token exchange failed: status %d", resp.StatusCode)
	}
	return &token, nil
}

type antigravityUserInfo struct {
	Email string `json:"email"`
}

func fetchAntigravityUserInfo(ctx context.Context, accessToken string, httpClient *http.Client) (*antigravityUserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return &antigravityUserInfo{}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity userinfo: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &antigravityUserInfo{}, nil
	}
	var info antigravityUserInfo
	if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode != nil {
		return nil, errDecode
	}
	return &info, nil
}

func buildAntigravityAuthURL(redirectURI, state string) string {
	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", antigravityClientID)
	params.Set("prompt", "consent")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(antigravityScopes, " "))
	params.Set("state", state)
	return "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode()
}

func sanitizeAntigravityFileName(email string) string {
	if strings.TrimSpace(email) == "" {
		return "antigravity.json"
	}
	replacer := strings.NewReplacer("@", "_", ".", "_")
	return fmt.Sprintf("antigravity-%s.json", replacer.Replace(email))
}

// Antigravity API constants for project discovery
const (
	antigravityAPIEndpoint    = "https://cloudcode-pa.googleapis.com"
	antigravityAPIVersion     = "v1internal"
	antigravityAPIUserAgent   = "google-api-nodejs-client/9.15.1"
	antigravityAPIClient      = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	antigravityClientMetadata = `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`
)

// AntigravityDiscoveryResult holds the project ID and tier discovered during auth.
type AntigravityDiscoveryResult struct {
	ProjectID string
	Tier      string
}

// FetchAntigravityProjectID exposes project discovery for external callers.
func FetchAntigravityProjectID(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	result, err := fetchAntigravityProjectAndTier(ctx, accessToken, httpClient)
	if err != nil {
		return "", err
	}
	return result.ProjectID, nil
}

// FetchAntigravityProjectAndTier exposes project and tier discovery for external callers.
func FetchAntigravityProjectAndTier(ctx context.Context, accessToken string, httpClient *http.Client) (*AntigravityDiscoveryResult, error) {
	return fetchAntigravityProjectAndTier(ctx, accessToken, httpClient)
}

// fetchAntigravityProjectAndTier retrieves the project ID and tier for the authenticated user via loadCodeAssist.
// This uses the same approach as Gemini CLI to get the cloudaicompanionProject.
func fetchAntigravityProjectAndTier(ctx context.Context, accessToken string, httpClient *http.Client) (*AntigravityDiscoveryResult, error) {
	// Call loadCodeAssist to get the project
	loadReqBody := map[string]any{
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", antigravityAPIEndpoint, antigravityAPIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityAPIUserAgent)
	req.Header.Set("X-Goog-Api-Client", antigravityAPIClient)
	req.Header.Set("Client-Metadata", antigravityClientMetadata)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity loadCodeAssist: close body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return nil, fmt.Errorf("decode response: %w", errDecode)
	}

	result := &AntigravityDiscoveryResult{}

	// Extract current tier from response
	if currentTier, ok := loadResp["currentTier"].(map[string]any); ok {
		if id, okID := currentTier["id"].(string); okID {
			result.Tier = strings.TrimSpace(id)
		}
	}

	// Extract projectID from response
	projectID := ""
	if id, ok := loadResp["cloudaicompanionProject"].(string); ok {
		projectID = strings.TrimSpace(id)
	}
	if projectID == "" {
		if projectMap, ok := loadResp["cloudaicompanionProject"].(map[string]any); ok {
			if id, okID := projectMap["id"].(string); okID {
				projectID = strings.TrimSpace(id)
			}
		}
	}

	if projectID == "" {
		// Determine tier for onboarding
		tierID := "legacy-tier"
		requiresUserProject := false
		if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
			for _, rawTier := range tiers {
				tier, okTier := rawTier.(map[string]any)
				if !okTier {
					continue
				}
				if isDefault, okDefault := tier["isDefault"].(bool); okDefault && isDefault {
					if id, okID := tier["id"].(string); okID && strings.TrimSpace(id) != "" {
						tierID = strings.TrimSpace(id)
						// Check if this tier requires user-defined project
						if userDefined, okUD := tier["userDefinedCloudaicompanionProject"].(bool); okUD {
							requiresUserProject = userDefined
						}
						break
					}
				}
			}
		}

		// Provide tier-aware error message for paid tiers
		if requiresUserProject && tierID != "free-tier" && tierID != "legacy-tier" {
			return nil, fmt.Errorf(
				"tier '%s' requires a user-provided project ID. "+
					"Set project_id in your credential file or use ANTIGRAVITY_PROJECT_ID environment variable",
				tierID,
			)
		}

		result.Tier = tierID
		projectID, err = antigravityOnboardUser(ctx, accessToken, tierID, httpClient)
		if err != nil {
			return nil, err
		}
		result.ProjectID = projectID
		return result, nil
	}

	result.ProjectID = projectID
	return result, nil
}

// antigravityOnboardUser attempts to fetch the project ID via onboardUser by polling for completion.
// It polls up to 150 times with 2s intervals (5 minutes total) to match LLM-API-Key-Proxy behavior.
// Returns an empty string when the operation times out or completes without a project ID.
func antigravityOnboardUser(ctx context.Context, accessToken, tierID string, httpClient *http.Client) (string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	fmt.Println("Antigravity: onboarding user...", tierID)
	requestBody := map[string]any{
		"tierId": tierID,
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	rawBody, errMarshal := json.Marshal(requestBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	// Increased from 5 attempts × 30s to 150 attempts × 2s (5 min total) for robustness
	const maxAttempts = 150
	const pollInterval = 2 * time.Second
	const requestTimeout = 30 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt%15 == 0 {
			// Log progress every 30 seconds
			log.Infof("Still waiting for onboarding completion... (%ds elapsed)", attempt*2)
		} else {
			log.Debugf("Polling attempt %d/%d", attempt, maxAttempts)
		}

		reqCtx := ctx
		var cancel context.CancelFunc
		if reqCtx == nil {
			reqCtx = context.Background()
		}
		reqCtx, cancel = context.WithTimeout(reqCtx, requestTimeout)

		endpointURL := fmt.Sprintf("%s/%s:onboardUser", antigravityAPIEndpoint, antigravityAPIVersion)
		req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
		if errRequest != nil {
			cancel()
			return "", fmt.Errorf("create request: %w", errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", antigravityAPIUserAgent)
		req.Header.Set("X-Goog-Api-Client", antigravityAPIClient)
		req.Header.Set("Client-Metadata", antigravityClientMetadata)

		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			cancel()
			return "", fmt.Errorf("execute request: %w", errDo)
		}

		bodyBytes, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("close body error: %v", errClose)
		}
		cancel()

		if errRead != nil {
			return "", fmt.Errorf("read response: %w", errRead)
		}

		if resp.StatusCode == http.StatusOK {
			var data map[string]any
			if errDecode := json.Unmarshal(bodyBytes, &data); errDecode != nil {
				return "", fmt.Errorf("decode response: %w", errDecode)
			}

			if done, okDone := data["done"].(bool); okDone && done {
				projectID := ""
				if responseData, okResp := data["response"].(map[string]any); okResp {
					switch projectValue := responseData["cloudaicompanionProject"].(type) {
					case map[string]any:
						if id, okID := projectValue["id"].(string); okID {
							projectID = strings.TrimSpace(id)
						}
					case string:
						projectID = strings.TrimSpace(projectValue)
					}
				}

				if projectID != "" {
					log.Infof("Successfully fetched project_id: %s", projectID)
					return projectID, nil
				}

				return "", fmt.Errorf("no project_id in response")
			}

			time.Sleep(pollInterval)
			continue
		}

		responsePreview := strings.TrimSpace(string(bodyBytes))
		if len(responsePreview) > 500 {
			responsePreview = responsePreview[:500]
		}

		responseErr := responsePreview
		if len(responseErr) > 200 {
			responseErr = responseErr[:200]
		}
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, responseErr)
	}

	return "", fmt.Errorf("onboarding timed out after %d attempts (%v)", maxAttempts, time.Duration(maxAttempts)*pollInterval)
}
