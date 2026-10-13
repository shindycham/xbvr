package vrsmash

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xbapps/xbvr/pkg/common"
	"github.com/xbapps/xbvr/pkg/config"
)

// userAPIBase is the account/auth API (REST_USER_TARGET + REST_USER_PATH).
// Variable so tests can point it at a local server.
var userAPIBase = "https://www.vrsmash.com/api"

// saveConfig persists a rotated refresh token. Overridable in tests so they do
// not write to the live database.
var saveConfig = config.SaveConfig

// The access token lives ~15 minutes and the refresh token ~24 hours (sliding:
// every refresh returns a new 24h refresh token). With automatic refresh on,
// xbvr keeps its own chain alive; if xbvr is idle for more than 24 hours the
// refresh token expires and the session must be re-seeded.
const refreshSafetyWindow = 2 * time.Minute

var tokenCache struct {
	mu     sync.Mutex
	atoken string
	expiry time.Time
}

func setCachedToken(token string, expiry time.Time) {
	tokenCache.mu.Lock()
	tokenCache.atoken = token
	tokenCache.expiry = expiry
	tokenCache.mu.Unlock()
}

// jwtExpiry reads the `exp` claim from a JWT. ok is false when the token is not
// a decodable JWT.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// tokenFromJar returns the atoken from the configured/Firefox cookie jar.
func tokenFromJar() (string, map[string]string) {
	jar, err := jarFn()
	if err != nil {
		return "", nil
	}
	return jar["atoken"], jar
}

func tokenValid(token string) bool {
	if token == "" {
		return false
	}
	exp, ok := jwtExpiry(token)
	if !ok {
		return true // opaque token; assume the server will reject if bad
	}
	return time.Until(exp) > refreshSafetyWindow
}

// currentAccessToken returns a usable atoken, refreshing it when automatic
// refresh is enabled and the current one has expired.
func currentAccessToken() (string, error) {
	tokenCache.mu.Lock()
	if tokenCache.atoken != "" && time.Until(tokenCache.expiry) > refreshSafetyWindow {
		token := tokenCache.atoken
		tokenCache.mu.Unlock()
		return token, nil
	}
	tokenCache.mu.Unlock()

	jarToken, jar := tokenFromJar()
	if tokenValid(jarToken) {
		if exp, ok := jwtExpiry(jarToken); ok {
			setCachedToken(jarToken, exp)
		} else {
			setCachedToken(jarToken, time.Now().Add(10*time.Minute))
		}
		return jarToken, nil
	}

	if !config.Config.VRSMashDownloader.AutoRefreshToken {
		return "", fmt.Errorf("vrsmash session expired (atoken). Open vrsmash.com in your browser, or enable 'keep the session alive' in Options -> VRSmash Downloader")
	}

	rtoken := strings.TrimSpace(config.Config.VRSMashDownloader.RefreshToken)
	if rtoken == "" && jar != nil {
		rtoken = jar["rtoken"]
	}
	if rtoken == "" {
		return "", fmt.Errorf("vrsmash automatic refresh is enabled but no refresh token is available (set one in Options -> VRSmash Downloader)")
	}

	atoken, newRtoken, err := refreshAccessToken(rtoken)
	if err != nil {
		return "", err
	}

	if exp, ok := jwtExpiry(atoken); ok {
		setCachedToken(atoken, exp)
	} else {
		setCachedToken(atoken, time.Now().Add(10*time.Minute))
	}

	if newRtoken != "" && newRtoken != config.Config.VRSMashDownloader.RefreshToken {
		config.Config.VRSMashDownloader.RefreshToken = newRtoken
		saveConfig()
	}
	return atoken, nil
}

// refreshAccessToken exchanges a refresh token for a new token pair.
func refreshAccessToken(rtoken string) (string, string, error) {
	endpoint := userAPIBase + "/v1/auth/refresh-token?refresh_token=" + url.QueryEscape(rtoken)

	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://www.vrsmash.com/")
	if jar, err := jarFn(); err == nil {
		for name, value := range jar {
			req.AddCookie(&http.Cookie{Name: name, Value: value})
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("vrsmash token refresh failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", "", fmt.Errorf("vrsmash refresh token was rejected (expired or invalid). Log in to vrsmash.com again and update the refresh token")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("vrsmash token refresh returned HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Data struct {
			Token struct {
				Atoken string `json:"atoken"`
				Rtoken string `json:"rtoken"`
			} `json:"token"`
		} `json:"data"`
		Token struct {
			Atoken string `json:"atoken"`
			Rtoken string `json:"rtoken"`
		} `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", "", fmt.Errorf("vrsmash token refresh returned an unexpected response: %w", err)
	}

	atoken := parsed.Data.Token.Atoken
	rtokenOut := parsed.Data.Token.Rtoken
	if atoken == "" {
		atoken = parsed.Token.Atoken
		rtokenOut = parsed.Token.Rtoken
	}
	if atoken == "" {
		return "", "", fmt.Errorf("vrsmash token refresh did not return an access token")
	}
	return atoken, rtokenOut, nil
}

// TokenStatus reports the current access token state for the options UI.
func TokenStatus() (expiry time.Time, valid bool, autoRefresh bool, hasRefreshToken bool) {
	if jarToken, _ := tokenFromJar(); jarToken != "" {
		if exp, ok := jwtExpiry(jarToken); ok {
			expiry = exp
			valid = time.Now().Before(exp)
		} else {
			valid = true
		}
	}

	tokenCache.mu.Lock()
	if tokenCache.atoken != "" && tokenCache.expiry.After(expiry) {
		expiry = tokenCache.expiry
		valid = time.Now().Before(expiry)
	}
	tokenCache.mu.Unlock()

	return expiry, valid,
		config.Config.VRSMashDownloader.AutoRefreshToken,
		strings.TrimSpace(config.Config.VRSMashDownloader.RefreshToken) != ""
}

// StartTokenKeepAlive refreshes the session periodically so the 24h refresh
// token window does not lapse while xbvr is idle. It is a no-op unless
// automatic refresh is enabled.
func StartTokenKeepAlive() {
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()

		for {
			if config.Config.VRSMashDownloader.AutoRefreshToken {
				if _, err := currentAccessToken(); err != nil {
					common.Log.WithField("task", "vrsmashToken").Warnf("session refresh failed: %v", err)
				} else {
					common.Log.WithField("task", "vrsmashToken").Debug("session refreshed")
				}
			}
			<-ticker.C
		}
	}()
}
