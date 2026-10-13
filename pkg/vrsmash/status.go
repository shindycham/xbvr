package vrsmash

import (
	"strings"

	"github.com/xbapps/xbvr/pkg/config"
)

// CookieSource reports how the VRsMash session will be obtained, for the
// options UI: "config" (collector config), "token" (automatic refresh from a
// stored rtoken), "firefox" (local Firefox profile) or "none".
func CookieSource() string {
	if len(configCookies()) > 0 {
		return "config"
	}
	if config.Config.VRSMashDownloader.AutoRefreshToken &&
		strings.TrimSpace(config.Config.VRSMashDownloader.RefreshToken) != "" {
		return "token"
	}
	if !config.Config.VRSMashDownloader.UseFirefoxCookies {
		return "none"
	}
	if jar, err := Jar(); err == nil && len(jar) > 0 {
		return "firefox"
	}
	return "none"
}
