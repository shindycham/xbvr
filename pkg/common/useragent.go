package common

// UserAgent is the browser-like User-Agent sent to sites and services that
// filter automated clients.
//
// It must not be left on a string that has been blocklisted. The previous
// value, "Chrome/73.0.3683.103", is rejected outright (HTTP 403) by
// realjamvr.com and porncornvr.com, which silently breaks both scrapers: every
// scheduled run fails with "Error visiting https://<site>/scenes Forbidden"
// and no scenes are ingested.
//
// The block is on the EXACT literal version string, not on "old browser"
// heuristics. Verified live: "Chrome/73.0.3683.104" (one digit different) and
// "Chrome/73.0.0.0" both return 200 from the same host that 403s
// "Chrome/73.0.3683.103". Because that exact string is XBVR's shipped default,
// every install sends it verbatim, making it a reliable fingerprint for the
// client - which is precisely what makes it worth blocklisting. Keep this a
// realistic, current browser UA; if a site starts 403ing again, check for a
// blocklisted-fingerprint 403 before assuming the site itself changed.
//
// The same applies with the opposite sign to leaving it unset: Go's default
// "Go-http-client/1.1" is refused by Cloudflare and by most Newznab indexers,
// so every outgoing request that is not to xbvr's own local services should
// send this.
const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"
