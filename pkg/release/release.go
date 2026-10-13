// Package release parses torrent and NZB release names into structured data.
//
// Adult VR releases are overwhelmingly named after one template:
//
//	Sitename.dd.mm.yyyy.actor.releasegroup
//
// with the technical details (resolution, codec, projection, container) wedged
// between the performers and the release group:
//
//	VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB
//	BadoinkVR.23.05.22.Alex.Coal.XXX.180.SBS.8K.HEVC-GROUP
//	CzechVR.12.03.2021.Some.Name.XXX.5K.H265-GROUP
//
// xbvr matches files to scenes by exact filename, which a release name never is,
// so the parsed fields here are what the library matcher works from instead.
// This package is deliberately free of any dependency on the database or the
// HTTP layer: it turns a string into a value, nothing more.
package release

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Release is a parsed release name. Every field is best effort: a name that does
// not follow the convention simply yields fewer populated fields.
type Release struct {
	// Raw is the original name, unmodified.
	Raw string `json:"raw"`

	// Site is the display form of the studio token(s), e.g. "VRBangers".
	Site string `json:"site"`
	// SiteKey is Site reduced to lowercase alphanumerics, e.g. "vrbangers".
	// Brand names normalise well ("NaughtyAmericaVR" -> "naughtyamericavr"),
	// which makes SiteKey the right thing to compare against xbvr site names.
	SiteKey string `json:"site_key"`

	// Date is the release date, zero when the name carries no recognisable date.
	Date time.Time `json:"date"`
	// HasDate reports whether Date was parsed.
	HasDate bool `json:"has_date"`
	// DateIsExact is true when the name pinned down a full day/month/year.
	DateIsExact bool `json:"date_is_exact"`

	// Performers holds the name tokens between the date and the first technical
	// marker, in their original case. The convention cannot tell a performer list
	// from a title, so these are simply "the name tokens", and the matcher tests
	// them against cast names and titles alike.
	Performers []string `json:"performers"`

	// Resolution is the vertical resolution in pixels when known, else 0.
	Resolution int `json:"resolution"`
	// Quality is the quality token exactly as it appeared, e.g. "2160p" or "8K".
	Quality string `json:"quality"`
	// Codec is a normalised codec name: "h264", "h265", "av1" or "vp9".
	Codec string `json:"codec"`
	// Projection is a normalised VR projection when one was recognised, e.g.
	// "180_sbs" or "mkx200".
	Projection string `json:"projection"`
	// Group is the release group, e.g. "WRB".
	Group string `json:"group"`
}

// mediaExtensions are stripped from the end of a name before parsing.
var mediaExtensions = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".m4v": true, ".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true,
	".flv": true, ".webm": true,
	// Indexer and Usenet artefacts, so "...2160p.MP4-WRB.nzb" still yields WRB.
	".nzb": true, ".torrent": true,
}

var (
	// A single token that already is a date, e.g. "20231014".
	reCompactDate = regexp.MustCompile(`^(19|20)(\d{2})(\d{2})(\d{2})$`)
	// Resolution tokens: "2160p".
	reResolutionP = regexp.MustCompile(`^(\d{3,4})p$`)
	// Resolution tokens: "4k".."9k".
	reResolutionK = regexp.MustCompile(`^([2-9])k$`)
	// Release group candidates.
	reGroupToken = regexp.MustCompile(`^[A-Za-z0-9]{2,15}$`)
	// A whole name wrapped in brackets: "[GROUP]".
	reBracket = regexp.MustCompile(`^\[([^\]]{2,20})\]$`)
	// Dash separated dates, rewritten to dot form before tokenising.
	reDashDateYMD = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
	reDashDateDMY = regexp.MustCompile(`^(\d{2})-(\d{2})-(\d{4})`)
)

// codecTokens maps a name token (lowercased) to a normalised codec.
var codecTokens = map[string]string{
	"h264": "h264", "x264": "h264", "avc": "h264", "264": "h264",
	"h265": "h265", "x265": "h265", "hevc": "h265", "265": "h265",
	"av1": "av1", "vp9": "vp9",
}

// projectionTokens are VR projection markers that stand on their own.
var projectionTokens = map[string]string{
	"mkx200": "mkx200", "mkx220": "mkx220", "rf52": "rf52",
	"vrca220": "vrca220", "vrca241": "vrca241",
	"fisheye": "fisheye", "fisheye190": "fisheye190", "f180": "fisheye",
	"180f": "fisheye", "eac": "eac",
}

// technicalTokens are markers that end the performer/title section. They are
// matched as whole tokens, so a performer named "April" is not swallowed.
var technicalTokens = map[string]bool{
	"xxx": true,
	"mp4": true, "mkv": true, "avi": true, "wmv": true, "mov": true, "m4v": true,
	"uhd": true, "hd": true, "sd": true, "fhd": true, "qhd": true, "dl": true,
	"hdr": true, "hdr10": true, "sdr": true, "10bit": true, "8bit": true,
	"remux": true, "web": true, "webrip": true, "webdl": true, "webdlrip": true,
	"bluray": true, "bdrip": true, "dvdrip": true, "hdtv": true, "rip": true,
	"sbs": true, "tb": true, "ou": true, "mono": true, "lr": true, "rl": true,
	"3dh": true, "3d": true, "vr": true, "180": true, "360": true,
	"oculus": true, "psvr": true, "gearvr": true, "smartphone": true,
	"pro": true, "trailer": true, "sample": true, "preview": true,
	"multi": true, "dual": true, "subs": true, "sub": true, "dub": true,
	"repack": true, "proper": true, "internal": true, "fixed": true,
	"original": true, "upscaled": true, "enhanced": true, "uncensored": true,
	"60fps": true, "30fps": true, "50fps": true, "24fps": true,
	"part1": true, "part2": true, "cd1": true, "cd2": true,
}

// performerStopWords are separators that appear inside a performer list but are
// not names themselves.
var performerStopWords = map[string]bool{
	"and": true, "with": true, "feat": true, "ft": true, "com": true, "www": true,
}

// NormalizeSiteKey reduces a studio or site name to its comparable form:
// lowercase, alphanumerics only.
func NormalizeSiteKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeToken reduces one token to its comparable form: lowercase,
// alphanumerics only.
func NormalizeToken(s string) string {
	return NormalizeSiteKey(s)
}

// ParseName turns a release name (with or without a media extension) into a
// Release. It never fails; unrecognised input yields a Release carrying just Raw
// and whatever could be salvaged.
func ParseName(name string) Release {
	r := Release{Raw: strings.TrimSpace(name)}
	if r.Raw == "" {
		return r
	}

	base := filepath.Base(r.Raw)

	// Strip a media extension so ".mp4-GROUP" does not swallow the group.
	if ext := strings.ToLower(filepath.Ext(base)); mediaExtensions[ext] {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}

	// A bracketed group may sit at either end: "[WRB]VRBangers..." / "...[WRB]".
	base, r.Group = extractBracketGroup(base)
	base = normalizeDashDates(base)
	body, group := extractDashGroup(base)
	if group != "" {
		r.Group = group
	}

	tokens := tokenize(body)
	if len(tokens) == 0 {
		return r
	}

	date, dateStart, dateEnd, hasDate, exact := findDate(tokens)
	r.Date, r.HasDate, r.DateIsExact = date, hasDate, exact

	// The studio is whatever precedes the date. Without a date there is no
	// reliable pivot, so fall back to the first token: it is nearly always the
	// studio even in unconventional names.
	siteEnd, nameStart := dateStart, dateEnd
	if !hasDate {
		siteEnd, nameStart = 1, 1
	}
	if siteEnd <= 0 {
		siteEnd = 1
	}
	siteTokens := tokens[:siteEnd]
	r.Site = strings.Join(siteTokens, " ")
	r.SiteKey = NormalizeSiteKey(strings.Join(siteTokens, ""))

	// Walk the remaining tokens once. Everything up to the first technical
	// marker is a performer or title word; everything after it is technical, but
	// we keep harvesting resolution/codec/projection from it because the
	// convention puts "XXX" before the quality ("...Actor.XXX.2160p").
	collectingNames := true
	for i := nameStart; i < len(tokens); i++ {
		tok := tokens[i]
		lower := strings.ToLower(tok)

		if c, ok := codecTokens[lower]; ok {
			if r.Codec == "" {
				r.Codec = c
			}
			collectingNames = false
			continue
		}
		if h, ok := resolutionOf(lower); ok {
			if r.Resolution == 0 {
				r.Resolution, r.Quality = h, tok
			}
			collectingNames = false
			continue
		}
		if p, ok := projectionTokens[lower]; ok {
			if r.Projection == "" {
				r.Projection = p
			}
			collectingNames = false
			continue
		}
		if isTechnical(lower) {
			if r.Projection == "" {
				r.Projection = projectionFrom(tokens, i)
			}
			collectingNames = false
			continue
		}
		if !collectingNames {
			continue
		}
		if performerStopWords[lower] || isNumeric(lower) {
			continue
		}
		r.Performers = append(r.Performers, tok)
	}

	return r
}

// ParseFilename is ParseName for a full path; only the base name is examined.
func ParseFilename(path string) Release {
	return ParseName(filepath.Base(path))
}

// resolutionOf maps a quality token to a vertical resolution.
func resolutionOf(tok string) (int, bool) {
	if m := reResolutionP.FindStringSubmatch(tok); m != nil {
		h, err := strconv.Atoi(m[1])
		if err == nil && h >= 240 && h <= 8640 {
			return h, true
		}
		return 0, false
	}
	if m := reResolutionK.FindStringSubmatch(tok); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			// The VR scene convention: 4K=2160, 5K=2700, 6K=3240, 8K=4320.
			return n * 540, true
		}
	}
	return 0, false
}

// isTechnical reports whether a lowercased token is a technical marker.
func isTechnical(tok string) bool {
	if tok == "" {
		return false
	}
	if technicalTokens[tok] {
		return true
	}
	// "180x180", "1920x1080", "4096x2040"
	if strings.Contains(tok, "x") {
		parts := strings.SplitN(tok, "x", 2)
		if len(parts) == 2 {
			_, e1 := strconv.Atoi(parts[0])
			_, e2 := strconv.Atoi(parts[1])
			if e1 == nil && e2 == nil {
				return true
			}
		}
	}
	return false
}

// projectionFrom inspects a technical marker and the token after it for a
// projection, e.g. "180" followed by "sbs".
func projectionFrom(tokens []string, i int) string {
	tok := strings.ToLower(tokens[i])
	switch tok {
	case "180", "360":
		if i+1 < len(tokens) {
			switch strings.ToLower(tokens[i+1]) {
			case "sbs":
				return tok + "_sbs"
			case "tb":
				return tok + "_tb"
			case "mono":
				return tok + "_mono"
			}
		}
		return tok
	}
	if p, ok := projectionTokens[tok]; ok {
		return p
	}
	return ""
}

// tokenize splits on the separators that appear in release names.
func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case '.', '_', ' ', '-', '+', '(', ')', ',':
			return true
		}
		return false
	})
}

// normalizeDashDates rewrites "2023-10-14" and "14-10-2023" to dot form so the
// dash-group extraction cannot mistake a date for a release group.
func normalizeDashDates(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if d, n := matchDashDate(runes[i:]); n > 0 {
			b.WriteString(d)
			i += n
			continue
		}
		b.WriteRune(runes[i])
		i++
	}
	return b.String()
}

func matchDashDate(s []rune) (string, int) {
	if len(s) < 10 {
		return "", 0
	}
	str := string(s)
	if m := reDashDateYMD.FindStringSubmatch(str); m != nil {
		return m[1] + "." + m[2] + "." + m[3], 10
	}
	if m := reDashDateDMY.FindStringSubmatch(str); m != nil {
		return m[1] + "." + m[2] + "." + m[3], 10
	}
	return "", 0
}

// extractBracketGroup pulls a leading or trailing "[GROUP]" off a name.
func extractBracketGroup(s string) (string, string) {
	trimmed := strings.TrimSpace(s)
	if m := reBracket.FindStringSubmatch(trimmed); m != nil {
		return "", m[1]
	}
	if strings.HasPrefix(trimmed, "[") {
		if end := strings.Index(trimmed, "]"); end > 1 {
			return strings.TrimSpace(trimmed[end+1:]), trimmed[1:end]
		}
	}
	if strings.HasSuffix(trimmed, "]") {
		if start := strings.LastIndex(trimmed, "["); start > 0 {
			return strings.TrimSpace(trimmed[:start]), trimmed[start+1 : len(trimmed)-1]
		}
	}
	return s, ""
}

// extractDashGroup splits "…2160p.MP4-WRB" into its body and group. A dash only
// marks a group when the tail is a single plausible token AND the body already
// ends in a technical marker (or the tail is entirely uppercase). Without that
// guard a name like "Jane-Doe" would lose half a performer name.
func extractDashGroup(s string) (string, string) {
	idx := strings.LastIndex(s, "-")
	if idx <= 0 || idx == len(s)-1 {
		return s, ""
	}
	body, tail := s[:idx], s[idx+1:]
	if !reGroupToken.MatchString(tail) {
		return s, ""
	}
	lower := strings.ToLower(tail)
	if _, ok := codecTokens[lower]; ok {
		return s, ""
	}
	if _, ok := resolutionOf(lower); ok {
		return s, ""
	}
	if isTechnical(lower) {
		return s, ""
	}
	allUpper := tail == strings.ToUpper(tail)
	if !allUpper && !endsWithTechnicalToken(body) {
		return s, ""
	}
	return body, tail
}

// endsWithTechnicalToken reports whether the last token of a name body is a
// technical marker. Real releases always put one before the group
// ("…2160p.MP4-WRB", "…2160p-GROUP", "…XXX-GROUP"), so requiring one is what
// stops "VRBangers.23.10.14.Jane-Doe" from being read as group "Doe".
func endsWithTechnicalToken(body string) bool {
	tokens := tokenize(body)
	if len(tokens) == 0 {
		return false
	}
	last := strings.ToLower(tokens[len(tokens)-1])
	if isTechnical(last) {
		return true
	}
	if _, ok := resolutionOf(last); ok {
		return true
	}
	if _, ok := codecTokens[last]; ok {
		return true
	}
	return false
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// findDate locates the release date in a token stream.
//
// It returns the date, the index it starts at, the index just past it, whether
// one was found, and whether it carried a full day/month/year.
//
// The convention is day-first ("dd.mm.yyyy"), but torrents commonly use
// "yy.mm.dd" as well. Where the two disagree the ambiguity is resolved by
// validity: a first component above 12 can only be a day, and a second component
// above 12 can only be a month in month-first order.
func findDate(tokens []string) (time.Time, int, int, bool, bool) {
	for i := 0; i < len(tokens); i++ {
		if m := reCompactDate.FindStringSubmatch(tokens[i]); m != nil {
			y, _ := strconv.Atoi(m[1] + m[2])
			mo, _ := strconv.Atoi(m[3])
			d, _ := strconv.Atoi(m[4])
			if t, ok := makeDate(y, mo, d); ok {
				return t, i, i + 1, true, true
			}
		}
	}
	for i := 0; i+2 < len(tokens); i++ {
		a, b, c := tokens[i], tokens[i+1], tokens[i+2]
		if !isNumeric(a) || !isNumeric(b) || !isNumeric(c) {
			continue
		}
		if t, ok := parseThreeNumbers(a, b, c); ok {
			return t, i, i + 3, true, true
		}
	}
	// Year-only form, e.g. "SomeScene.2023.2160p".
	for i := 0; i < len(tokens); i++ {
		if len(tokens[i]) != 4 || !isNumeric(tokens[i]) {
			continue
		}
		if n, _ := strconv.Atoi(tokens[i]); n >= 1970 && n <= time.Now().Year()+1 {
			return time.Date(n, 1, 1, 0, 0, 0, 0, time.UTC), i, i + 1, true, false
		}
	}
	return time.Time{}, 0, 0, false, false
}

// parseThreeNumbers interprets three consecutive numeric tokens as a date.
func parseThreeNumbers(a, b, c string) (time.Time, bool) {
	na, _ := strconv.Atoi(a)
	nb, _ := strconv.Atoi(b)
	nc, _ := strconv.Atoi(c)

	// Four digit year in front: yyyy.mm.dd
	if len(a) == 4 && na >= 1970 && na <= time.Now().Year()+1 {
		return makeDate(na, nb, nc)
	}
	// Four digit year at the end: dd.mm.yyyy, the documented convention.
	if len(c) == 4 && nc >= 1970 && nc <= time.Now().Year()+1 {
		if t, ok := makeDate(nc, nb, na); ok {
			return t, true
		}
		// Month out of range day-first: fall back to month-first.
		return makeDate(nc, na, nb)
	}
	// All three components short, so the order is genuinely ambiguous. Enumerate
	// the readings that can be valid and pick the most recent year.
	//
	// "23.10.14" is the case that forces this: reading it day-first gives
	// 2014-10-23, while reading it year-first gives 2023-10-14. Both are
	// internally consistent, and the tie is broken by recency because these are
	// VR releases from the last few years, not a 2014 back catalogue.
	//
	// The enumeration order breaks ties within the same year in favour of the
	// documented day-first convention.
	candidates := []struct{ y, m, d int }{
		{fullYear(nc), nb, na}, // dd.mm.yy
		{fullYear(nc), na, nb}, // mm.dd.yy
		{fullYear(na), nb, nc}, // yy.mm.dd
	}
	var best time.Time
	found := false
	for _, c := range candidates {
		t, ok := makeDate(c.y, c.m, c.d)
		if !ok {
			continue
		}
		if !found || t.After(best) {
			best, found = t, true
		}
	}
	return best, found
}

// fullYear expands a two digit year relative to the current date.
func fullYear(y int) int {
	if y >= 100 {
		return y
	}
	if y <= time.Now().Year()%100+1 {
		return 2000 + y
	}
	return 1900 + y
}

func makeDate(year, month, day int) (time.Time, bool) {
	if year < 1970 || year > time.Now().Year()+2 {
		return time.Time{}, false
	}
	if month < 1 || month > 12 {
		return time.Time{}, false
	}
	if day < 1 || day > 31 {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	// Reject overflow such as 31 February.
	if t.Day() != day || int(t.Month()) != month {
		return time.Time{}, false
	}
	return t, true
}
