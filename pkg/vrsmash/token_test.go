package vrsmash

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbapps/xbvr/pkg/config"
)

func fakeJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return header + "." + payload + ".sig"
}

func resetTokenCache() {
	tokenCache.mu.Lock()
	tokenCache.atoken = ""
	tokenCache.expiry = time.Time{}
	tokenCache.mu.Unlock()
}

func TestJWTExpiry(t *testing.T) {
	exp := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	got, ok := jwtExpiry(fakeJWT(exp))
	if !ok || !got.Equal(exp) {
		t.Fatalf("jwtExpiry = %v, %v; want %v, true", got, ok, exp)
	}
	if _, ok := jwtExpiry("not-a-jwt"); ok {
		t.Error("expected ok=false for a non-JWT")
	}
	if _, ok := jwtExpiry(""); ok {
		t.Error("expected ok=false for an empty token")
	}
}

func TestTokenValid(t *testing.T) {
	if !tokenValid(fakeJWT(time.Now().Add(time.Hour))) {
		t.Error("expected a token expiring in an hour to be valid")
	}
	if tokenValid(fakeJWT(time.Now().Add(time.Minute))) {
		t.Error("expected a token inside the safety window to be treated as expired")
	}
	if tokenValid("") {
		t.Error("expected an empty token to be invalid")
	}
	// Opaque tokens cannot be inspected and are assumed usable.
	if !tokenValid("opaque-token") {
		t.Error("expected an opaque token to be assumed valid")
	}
}

func TestRefreshAccessToken(t *testing.T) {
	origBase, origJar := userAPIBase, jarFn
	defer func() {
		userAPIBase, jarFn = origBase, origJar
	}()
	jarFn = func() (map[string]string, error) {
		return map[string]string{"cf_clearance": "x"}, nil
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.RawQuery, "refresh_token=rt-123") {
			t.Errorf("query = %q, want refresh_token=rt-123", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":{"token":{"atoken":"AT","rtoken":"RT2"}}}`))
	}))
	defer srv.Close()
	userAPIBase = srv.URL

	at, rt, err := refreshAccessToken("rt-123")
	if err != nil || at != "AT" || rt != "RT2" {
		t.Fatalf("refreshAccessToken = %q, %q, %v", at, rt, err)
	}

	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer reject.Close()
	userAPIBase = reject.URL

	if _, _, err := refreshAccessToken("bad"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected a rejected error, got %v", err)
	}
}

func TestCurrentAccessTokenRefreshesAndPersists(t *testing.T) {
	origCfg := config.Config
	origBase, origJar, origSave := userAPIBase, jarFn, saveConfig
	defer func() {
		config.Config = origCfg
		userAPIBase, jarFn, saveConfig = origBase, origJar, origSave
		resetTokenCache()
	}()

	newAtoken := fakeJWT(time.Now().Add(time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"token":{"atoken":"` + newAtoken + `","rtoken":"rotated-rt"}}}`))
	}))
	defer srv.Close()

	userAPIBase = srv.URL
	// An empty jar forces the refresh path.
	jarFn = func() (map[string]string, error) { return map[string]string{}, nil }
	saves := 0
	saveConfig = func() { saves++ }
	resetTokenCache()

	config.Config.VRSMashDownloader.AutoRefreshToken = true
	config.Config.VRSMashDownloader.RefreshToken = "initial-rt"

	at, err := currentAccessToken()
	if err != nil {
		t.Fatalf("currentAccessToken: %v", err)
	}
	if at != newAtoken {
		t.Errorf("access token = %q, want the refreshed token", at)
	}
	if config.Config.VRSMashDownloader.RefreshToken != "rotated-rt" {
		t.Errorf("refresh token = %q, want rotated-rt", config.Config.VRSMashDownloader.RefreshToken)
	}
	if saves == 0 {
		t.Error("expected the rotated refresh token to be persisted")
	}
}

func TestCurrentAccessTokenRefusedWhenDisabled(t *testing.T) {
	origCfg := config.Config
	origJar := jarFn
	defer func() {
		config.Config = origCfg
		jarFn = origJar
		resetTokenCache()
	}()

	jarFn = func() (map[string]string, error) {
		return map[string]string{"atoken": fakeJWT(time.Now().Add(-time.Minute))}, nil
	}
	resetTokenCache()

	config.Config.VRSMashDownloader.AutoRefreshToken = false
	config.Config.VRSMashDownloader.RefreshToken = ""

	_, err := currentAccessToken()
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("expected a session expired error, got %v", err)
	}
}
