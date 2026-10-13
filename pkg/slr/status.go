package slr

import "github.com/xbapps/xbvr/pkg/config"

// CookieSource reports where the SLR cookies would come from, for the options
// UI: "config", "firefox" or "none".
func CookieSource() string {
	if cookieHeaderFromConfig() != "" {
		return "config"
	}
	if config.Config.SLRDownloader.UseFirefoxCookies {
		if header, err := cookieHeaderFromFirefox(); err == nil && header != "" {
			return "firefox"
		}
	}
	return "none"
}
