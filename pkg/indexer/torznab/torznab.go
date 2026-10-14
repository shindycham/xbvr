// Package torznab implements the Torznab and Newznab indexer protocols.
//
// Both dialects are the same RSS-shaped API. They differ only in the XML
// namespace they use for their "attr" elements and in which attributes are
// present: Torznab adds torrent metadata (seeders, peers, infohash, magnet)
// while Newznab describes Usenet releases. The parsing logic is shared; the
// configured protocol only selects the default value of Release.Protocol.
//
// The package registers itself for both protocol names from init, so the
// wiring import in pkg/tasks merely needs a blank import.
package torznab

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xbapps/xbvr/pkg/common"
	"github.com/xbapps/xbvr/pkg/indexer"
)

func init() {
	indexer.Register(indexer.ProtocolTorznab, newClient)
	indexer.Register(indexer.ProtocolNewznab, newClient)
}

const (
	// requestTimeout bounds every HTTP round trip.
	requestTimeout = 20 * time.Second
	// defaultLimit is the result cap used when a Query does not set one.
	defaultLimit = 100
	// maxBodyBytes caps how much of a response body is read into memory.
	maxBodyBytes = 32 << 20
	// errorBodyBytes caps how much of a failing response is echoed in errors.
	errorBodyBytes = 512
)

// unmarshalLoose decodes an XML document without rejecting the undefined HTML
// entities ("&nbsp;") that real indexer feeds carry; encoding/xml otherwise
// fails the whole document on the first one it meets.
func unmarshalLoose(body []byte, v any) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	return dec.Decode(v)
}

// client is the shared Torznab/Newznab implementation.
type client struct {
	cfg indexer.Config
	// protocol is the default protocol for releases: "torrent" or "nzb".
	protocol string
	// typeName is the registered protocol id, returned by Type.
	typeName string
	http     *http.Client
}

// newClient builds a client from cfg. It is registered for both torznab and
// newznab; cfg.Type only decides the default release protocol.
func newClient(cfg indexer.Config) (indexer.Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("torznab: indexer URL is required")
	}

	protocol := "torrent"
	typeName := cfg.Type
	if cfg.Type == indexer.ProtocolNewznab {
		protocol = "nzb"
	} else if cfg.Type == "" {
		typeName = indexer.ProtocolTorznab
	}

	return &client{
		cfg:      cfg,
		protocol: protocol,
		typeName: typeName,
		http:     &http.Client{},
	}, nil
}

func (c *client) ID() string { return c.cfg.ID }

func (c *client) Type() string { return c.typeName }

// Name returns the configured display name, falling back to the indexer host.
func (c *client) Name() string {
	if c.cfg.Name != "" {
		return c.cfg.Name
	}
	if u, err := url.Parse(c.cfg.URL); err == nil {
		if host := u.Hostname(); host != "" {
			return host
		}
	}
	return c.cfg.URL
}

// Test verifies that the endpoint answers with a well-formed caps document.
// Caps does not require a valid API key to parse, but an <error> element,
// including one nested inside <caps>, is reported by do.
func (c *client) Test(ctx context.Context) error {
	body, err := c.do(ctx, url.Values{"t": {"caps"}})
	if err != nil {
		return err
	}
	var root struct {
		XMLName xml.Name
	}
	if err := unmarshalLoose(body, &root); err != nil {
		return fmt.Errorf("torznab: caps response is not well-formed XML: %w", err)
	}
	if root.XMLName.Local != "caps" {
		return fmt.Errorf("torznab: unexpected caps response root element %q", root.XMLName.Local)
	}
	return nil
}

// Caps returns the indexer's categories with their children populated.
func (c *client) Caps(ctx context.Context) ([]indexer.Category, error) {
	body, err := c.do(ctx, url.Values{"t": {"caps"}})
	if err != nil {
		return nil, err
	}
	var doc capsDoc
	if err := unmarshalLoose(body, &doc); err != nil {
		return nil, fmt.Errorf("torznab: parsing caps response: %w", err)
	}
	out := make([]indexer.Category, 0, len(doc.Categories.Categories))
	for _, cat := range doc.Categories.Categories {
		out = append(out, cat.toCategory())
	}
	return out, nil
}

// Search runs one t=search query.
func (c *client) Search(ctx context.Context, q indexer.Query) ([]indexer.Release, error) {
	params := url.Values{}
	params.Set("t", "search")
	if q.Q != "" {
		params.Set("q", q.Q)
	}
	cats := q.Categories
	if len(cats) == 0 {
		cats = c.cfg.Categories
	}
	if len(cats) > 0 {
		params.Set("cat", strings.Join(cats, ","))
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	params.Set("limit", strconv.Itoa(limit))

	body, err := c.do(ctx, params)
	if err != nil {
		return nil, err
	}

	var doc rssDoc
	if err := unmarshalLoose(body, &doc); err != nil {
		return nil, fmt.Errorf("torznab: parsing search response: %w", err)
	}

	releases := make([]indexer.Release, 0, len(doc.Channel.Items))
	for _, item := range doc.Channel.Items {
		rel := c.releaseFromItem(item)
		if q.Types != "" && q.Types != rel.Protocol {
			continue
		}
		releases = append(releases, rel)
	}
	return releases, nil
}

// releaseFromItem converts one RSS item into a Release.
func (c *client) releaseFromItem(item rssItem) indexer.Release {
	attrs := make(map[string]string, len(item.Attrs))
	for _, a := range item.Attrs {
		name := strings.ToLower(strings.TrimSpace(a.Name))
		if name == "" {
			continue
		}
		if _, ok := attrs[name]; !ok {
			attrs[name] = a.Value
		}
	}

	link := strings.TrimSpace(item.Link)
	magnet := strings.TrimSpace(attrs["magneturl"])
	if magnet == "" && isMagnet(link) {
		magnet = link
	}

	download := strings.TrimSpace(item.Enclosure.URL)
	if download == "" || isMagnet(download) {
		download = ""
	}
	if download == "" && link != "" && !isMagnet(link) {
		download = link
	}
	// Some indexers carry the download URL in <guid> and only a human-readable
	// details page in <link>, with no <enclosure> at all. A guid that is not an
	// http(s) URL (a bare id or a urn:) is not downloadable and is ignored.
	if download == "" {
		if guid := strings.TrimSpace(item.GUID); isHTTPURL(guid) {
			download = guid
		}
	}

	infoHash := strings.ToUpper(strings.TrimSpace(attrs["infohash"]))

	size := int64(0)
	for _, candidate := range []string{attrs["size"], item.Size, item.Enclosure.Length} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if n, err := strconv.ParseInt(candidate, 10, 64); err == nil {
			size = n
			break
		}
	}

	seeders := -1
	if v, ok := attrs["seeders"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			seeders = n
		}
	}

	leechers := -1
	if v, ok := attrs["leechers"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			leechers = n
		}
	} else if v, ok := attrs["peers"]; ok {
		if peers, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			if seeders >= 0 {
				leechers = peers - seeders
			} else {
				leechers = peers
			}
			if leechers < 0 {
				leechers = 0
			}
		}
	}

	grabs := 0
	if v, ok := attrs["grabs"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			grabs = n
		}
	}

	protocol := c.protocol
	if magnet != "" || infoHash != "" {
		protocol = "torrent"
	}

	return indexer.Release{
		Indexer:     c.Name(),
		IndexerID:   c.cfg.ID,
		Title:       strings.TrimSpace(item.Title),
		DownloadURL: download,
		MagnetURL:   magnet,
		InfoHash:    infoHash,
		Size:        size,
		Seeders:     seeders,
		Leechers:    leechers,
		Categories:  categories(item),
		PublishedAt: parsePubDate(item.PubDate),
		Protocol:    protocol,
		Grabs:       grabs,
	}
}

// categories collects <category> elements and category attributes, in order
// and de-duplicated. A release may carry several category attributes, so the
// attributes are scanned directly rather than through the single-value map.
func categories(item rssItem) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, v := range item.Categories {
		add(v)
	}
	for _, a := range item.Attrs {
		if strings.EqualFold(strings.TrimSpace(a.Name), "category") {
			add(a.Value)
		}
	}
	return out
}

// parsePubDate accepts the date layouts indexers actually emit.
func parsePubDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func isMagnet(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "magnet:")
}

// isHTTPURL reports whether s is an absolute http(s) URL. A bare id or a
// urn: value parses with a different (or empty) scheme and is not downloadable.
func isHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// do builds the request URL, executes the GET, validates the status and body
// and returns the raw response. It is the only place that touches the network.
func (c *client) do(ctx context.Context, params url.Values) ([]byte, error) {
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("torznab: invalid indexer URL: %w", err)
	}

	query := u.Query()
	for key, values := range params {
		for _, value := range values {
			query.Set(key, value)
		}
	}
	if c.cfg.APIKey != "" {
		query.Set("apikey", c.cfg.APIKey)
	}
	query.Set("extended", "1")
	u.RawQuery = query.Encode()

	requestURL := u.String()
	safeURL := c.redactURL(requestURL)

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("torznab: building request for %s: %s", safeURL, c.redact(err.Error()))
	}
	req.Header.Set("Accept", "application/xml, text/xml, application/rss+xml, */*")
	// Without this Go sends "Go-http-client/1.1", which Cloudflare and most
	// Newznab indexers refuse outright with a 403 HTML page.
	req.Header.Set("User-Agent", common.UserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("torznab: request to %s failed: %s", safeURL, c.redact(err.Error()))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("torznab: reading response from %s: %s", safeURL, c.redact(err.Error()))
	}

	// A web page where a feed belongs means something in front of the indexer
	// answered instead of it. That happens on any status, including 200 for a
	// Cloudflare challenge, so it is checked before the status.
	if looksLikeHTML(resp.Header.Get("Content-Type"), body) {
		return nil, fmt.Errorf("torznab: %s answered with a web page rather than a feed (HTTP %d). "+
			"A bot filter such as Cloudflare is blocking xbvr; routing this indexer through "+
			"Prowlarr or Jackett avoids the block",
			safeURL, resp.StatusCode)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// A 404 is nearly always a URL that looks plausible but is not the API
		// root, so name the trap rather than echoing a bare "Not Found".
		hint := ""
		if resp.StatusCode == http.StatusNotFound {
			hint = ". Check the indexer URL: most indexers serve the API at a plain /api path with no version segment"
		}
		return nil, fmt.Errorf("torznab: %s returned HTTP %d: %s%s",
			safeURL, resp.StatusCode, c.redact(truncate(body, errorBodyBytes)), hint)
	}

	if e := findIndexerError(body); e != nil {
		return nil, fmt.Errorf("torznab: indexer error: %s", c.redact(e.Error()))
	}
	return body, nil
}

// looksLikeHTML reports whether a response is a web page rather than the RSS or
// XML an indexer is supposed to return. Both the content type and the first
// bytes are inspected, because a bot filter's page is not always labelled
// honestly.
func looksLikeHTML(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(truncate(body, 256)))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// redactURL replaces the apikey query parameter and drops any userinfo so a URL
// can be shown in an error without leaking credentials.
func (c *client) redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return c.redact(raw)
	}
	if u.User != nil {
		// A configured http://user:pass@host URL must not be echoed verbatim.
		u.User = nil
	}
	q := u.Query()
	if _, ok := q["apikey"]; ok {
		q.Set("apikey", "REDACTED")
		u.RawQuery = q.Encode()
	}
	return c.redact(u.String())
}

// redact removes the configured API key from arbitrary text.
func (c *client) redact(s string) string {
	if c.cfg.APIKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.cfg.APIKey, "REDACTED")
}

// truncate limits a byte slice used in an error message.
func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…(truncated)"
}

// indexerError is an <error> element returned with an otherwise successful
// HTTP status.
type indexerError struct {
	Code        string
	Description string
	Message     string
}

func (e *indexerError) Error() string {
	desc := e.Description
	if desc == "" {
		desc = e.Message
	}
	switch {
	case e.Code != "" && desc != "":
		return fmt.Sprintf("code %s: %s", e.Code, desc)
	case e.Code != "":
		return fmt.Sprintf("code %s", e.Code)
	case desc != "":
		return desc
	default:
		return "unknown indexer error"
	}
}

// findIndexerError scans an XML body for a document-level <error> element. Both
// a bare root <error> (depth 1) and one nested in <caps> (depth 2) are
// detected; an <error> nested deeper, for example inside an <item> or
// <torrent>, is release data and must not reject the whole response.
func findIndexerError(body []byte) *indexerError {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local != "error" || depth > 2 {
				continue
			}
			e := &indexerError{}
			for _, attr := range t.Attr {
				switch attr.Name.Local {
				case "code":
					e.Code = attr.Value
				case "description":
					e.Description = attr.Value
				}
			}
			var text []string
			for {
				inner, err := dec.Token()
				if err != nil {
					break
				}
				switch it := inner.(type) {
				case xml.CharData:
					text = append(text, string(it))
				case xml.EndElement:
					if it.Name.Local == "error" {
						e.Message = strings.TrimSpace(strings.Join(text, " "))
						return e
					}
				}
			}
			e.Message = strings.TrimSpace(strings.Join(text, " "))
			return e
		case xml.EndElement:
			depth--
		}
	}
}

// --- XML documents -------------------------------------------------------

// capsDoc mirrors a Newznab/Torznab <caps> document. Element names are matched
// by local name, so both the torznab: and newznab: namespaces work.
type capsDoc struct {
	XMLName    xml.Name `xml:"caps"`
	Categories struct {
		Categories []capsCategory `xml:"category"`
	} `xml:"categories"`
}

type capsCategory struct {
	ID      string         `xml:"id,attr"`
	Name    string         `xml:"name,attr"`
	Subcats []capsCategory `xml:"subcat"`
}

func (c capsCategory) toCategory() indexer.Category {
	out := indexer.Category{ID: strings.TrimSpace(c.ID), Name: strings.TrimSpace(c.Name)}
	for _, sub := range c.Subcats {
		out.Children = append(out.Children, sub.toCategory())
	}
	return out
}

// rssDoc mirrors a search response. The <torznab:response>/<newznab:response>
// element is intentionally ignored.
type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

// rssItem is deliberately permissive: attr elements from both dialects are
// collected by local name, and the attribute name/value pairs are interpreted
// in Go.
type rssItem struct {
	Title      string       `xml:"title"`
	GUID       string       `xml:"guid"`
	Link       string       `xml:"link"`
	Comments   string       `xml:"comments"`
	PubDate    string       `xml:"pubDate"`
	Size       string       `xml:"size"`
	Enclosure  rssEnclosure `xml:"enclosure"`
	Categories []string     `xml:"category"`
	Attrs      []rssAttr    `xml:"attr"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

// rssAttr matches <torznab:attr> and <newznab:attr> alike: the struct tag has
// no namespace, so encoding/xml matches the local name "attr".
type rssAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}
