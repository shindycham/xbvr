package torznab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbapps/xbvr/pkg/indexer"
)

// newTestClient builds a torznab client pointed at srv.
func newTestClient(t *testing.T, srv *httptest.Server, cfg indexer.Config) indexer.Client {
	t.Helper()
	if cfg.Type == "" {
		cfg.Type = indexer.ProtocolTorznab
	}
	cfg.URL = srv.URL
	c, err := newClient(cfg)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	return c
}

func TestNewClientRequiresURL(t *testing.T) {
	if _, err := newClient(indexer.Config{Type: indexer.ProtocolTorznab}); err == nil {
		t.Fatal("expected an error for an empty URL")
	}
}

func TestRegisteredForBothProtocols(t *testing.T) {
	for _, proto := range []string{indexer.ProtocolTorznab, indexer.ProtocolNewznab} {
		if _, err := indexer.New(indexer.Config{Type: proto, URL: "http://127.0.0.1:1/api"}); err != nil {
			t.Errorf("indexer.New(%q): %v", proto, err)
		}
	}
}

const capsBody = `<caps xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <categories>
    <torznab:category id="6000" name="XXX">
      <subcat id="6010" name="XXX/WEB-DL"/>
      <torznab:subcat id="6020" name="XXX/DVD"/>
    </torznab:category>
    <category id="5000" name="TV">
      <subcat id="5040" name="TV/HD"/>
    </category>
  </categories>
</caps>`

func TestCapsParsing(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(capsBody))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1", Name: "Prowlarr"})

	cats, err := c.Caps(context.Background())
	if err != nil {
		t.Fatalf("Caps: %v", err)
	}
	want := []indexer.Category{
		{ID: "6000", Name: "XXX", Children: []indexer.Category{
			{ID: "6010", Name: "XXX/WEB-DL"},
			{ID: "6020", Name: "XXX/DVD"},
		}},
		{ID: "5000", Name: "TV", Children: []indexer.Category{
			{ID: "5040", Name: "TV/HD"},
		}},
	}
	if !reflect.DeepEqual(cats, want) {
		t.Errorf("Caps = %#v, want %#v", cats, want)
	}

	if got := gotQuery.Get("t"); got != "caps" {
		t.Errorf("t = %q, want %q", got, "caps")
	}
	if got := gotQuery.Get("extended"); got != "1" {
		t.Errorf("extended = %q, want %q", got, "1")
	}
}

func TestCapsWithoutAPIKeySucceeds(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(capsBody))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if _, ok := gotQuery["apikey"]; ok {
		t.Errorf("apikey must be omitted when unset, got %q", gotQuery.Get("apikey"))
	}
}

func TestCapsRejectsErrorElement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<caps><error code="201" description="bad caps"/></caps>`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected Test to fail on a nested <error> element")
	} else if !strings.Contains(err.Error(), "201") || !strings.Contains(err.Error(), "bad caps") {
		t.Errorf("error %q should contain the code and description", err)
	}
}

const searchBody = `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
 <channel>
  <item>
    <title>VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB</title>
    <guid>https://indexer/details/abc123</guid>
    <link>https://indexer/download/abc123.torrent</link>
    <comments/>
    <pubDate>Sat, 14 Oct 2023 09:00:00 +0000</pubDate>
    <size>3930769406</size>
    <enclosure url="https://indexer/download/abc123.torrent" length="3930769406" type="application/x-bittorrent"/>
    <category>6000</category>
    <torznab:attr name="seeders" value="12"/>
    <torznab:attr name="peers" value="20"/>
    <torznab:attr name="infohash" value="abc123def456"/>
    <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:abc123def456"/>
    <torznab:attr name="category" value="6000"/>
    <torznab:attr name="category" value="6010"/>
  </item>
  <item>
    <title>Some.Scene.23.11.01.1080p.WEB-DL</title>
    <link>https://indexer/download/def456.nzb</link>
    <pubDate>Wed, 01 Nov 2023 12:30:00 +0000</pubDate>
    <size>1073741824</size>
    <enclosure url="https://indexer/download/def456.nzb" length="1073741824" type="application/x-nzb"/>
    <newznab:attr name="category" value="5000"/>
    <newznab:attr name="size" value="1073741824"/>
    <newznab:attr name="grabs" value="7"/>
  </item>
  <item>
    <title>Magnet.Only.Release</title>
    <link>magnet:?xt=urn:btih:CAFEBABE</link>
    <pubDate>Fri, 13 Oct 2023 08:00:00 +0000</pubDate>
  </item>
  <item>
    <title>Attr.Magnet.Release</title>
    <link>https://indexer/details/ghi789</link>
    <pubDate>not a date</pubDate>
    <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:0123"/>
    <torznab:attr name="seeders" value="3"/>
    <torznab:attr name="leechers" value="5"/>
    <torznab:attr name="peers" value="99"/>
  </item>
 </channel>
 <newznab:response offset="0" total="42"/>
</rss>`

func TestSearchParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(searchBody))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1", Name: "Prowlarr"})

	rels, err := c.Search(context.Background(), indexer.Query{Q: "jane"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rels) != 4 {
		t.Fatalf("got %d releases, want 4", len(rels))
	}

	t.Run("enclosure with magnet attr and infohash", func(t *testing.T) {
		r := rels[0]
		if r.Title != "VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB" {
			t.Errorf("Title = %q", r.Title)
		}
		if r.DownloadURL != "https://indexer/download/abc123.torrent" {
			t.Errorf("DownloadURL = %q", r.DownloadURL)
		}
		if r.MagnetURL != "magnet:?xt=urn:btih:abc123def456" {
			t.Errorf("MagnetURL = %q", r.MagnetURL)
		}
		if r.InfoHash != "ABC123DEF456" {
			t.Errorf("InfoHash = %q, want uppercased", r.InfoHash)
		}
		if r.Size != 3930769406 {
			t.Errorf("Size = %d", r.Size)
		}
		if r.Seeders != 12 {
			t.Errorf("Seeders = %d", r.Seeders)
		}
		if r.Leechers != 8 {
			t.Errorf("Leechers = %d, want peers-seeders", r.Leechers)
		}
		if want := []string{"6000", "6010"}; !reflect.DeepEqual(r.Categories, want) {
			t.Errorf("Categories = %v, want %v", r.Categories, want)
		}
		if !r.PublishedAt.Equal(time.Date(2023, 10, 14, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("PublishedAt = %v", r.PublishedAt)
		}
		if r.Protocol != "torrent" {
			t.Errorf("Protocol = %q", r.Protocol)
		}
		if r.Indexer != "Prowlarr" || r.IndexerID != "idx-1" {
			t.Errorf("Indexer/IndexerID = %q/%q", r.Indexer, r.IndexerID)
		}
	})

	t.Run("newznab namespace attributes", func(t *testing.T) {
		r := rels[1]
		if r.DownloadURL != "https://indexer/download/def456.nzb" {
			t.Errorf("DownloadURL = %q", r.DownloadURL)
		}
		if r.MagnetURL != "" {
			t.Errorf("MagnetURL = %q, want empty", r.MagnetURL)
		}
		if r.Size != 1073741824 {
			t.Errorf("Size = %d", r.Size)
		}
		if r.Grabs != 7 {
			t.Errorf("Grabs = %d", r.Grabs)
		}
		if want := []string{"5000"}; !reflect.DeepEqual(r.Categories, want) {
			t.Errorf("Categories = %v, want %v", r.Categories, want)
		}
		if !r.PublishedAt.Equal(time.Date(2023, 11, 1, 12, 30, 0, 0, time.UTC)) {
			t.Errorf("PublishedAt = %v", r.PublishedAt)
		}
		if r.Seeders != -1 || r.Leechers != -1 {
			t.Errorf("Seeders/Leechers = %d/%d, want -1/-1", r.Seeders, r.Leechers)
		}
	})

	t.Run("link magnet only", func(t *testing.T) {
		r := rels[2]
		if r.DownloadURL != "" {
			t.Errorf("DownloadURL = %q, want empty for a magnet-only item", r.DownloadURL)
		}
		if r.MagnetURL != "magnet:?xt=urn:btih:CAFEBABE" {
			t.Errorf("MagnetURL = %q", r.MagnetURL)
		}
		if r.InfoHash != "" {
			t.Errorf("InfoHash = %q, want empty", r.InfoHash)
		}
		if r.Size != 0 {
			t.Errorf("Size = %d, want 0", r.Size)
		}
		if r.Protocol != "torrent" {
			t.Errorf("Protocol = %q, want torrent", r.Protocol)
		}
	})

	t.Run("magnet attribute and no size", func(t *testing.T) {
		r := rels[3]
		if r.DownloadURL != "https://indexer/details/ghi789" {
			t.Errorf("DownloadURL = %q", r.DownloadURL)
		}
		if r.MagnetURL != "magnet:?xt=urn:btih:0123" {
			t.Errorf("MagnetURL = %q", r.MagnetURL)
		}
		if r.Size != 0 {
			t.Errorf("Size = %d, want 0 when nothing reports it", r.Size)
		}
		if r.Seeders != 3 {
			t.Errorf("Seeders = %d", r.Seeders)
		}
		if r.Leechers != 5 {
			t.Errorf("Leechers = %d, want the explicit leechers attribute", r.Leechers)
		}
		if !r.PublishedAt.IsZero() {
			t.Errorf("PublishedAt = %v, want zero", r.PublishedAt)
		}
		if r.Protocol != "torrent" {
			t.Errorf("Protocol = %q, want torrent because of the magnet", r.Protocol)
		}
	})
}

func TestNewznabProtocolAndOverrides(t *testing.T) {
	body := `<rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
 <channel>
  <item>
    <title>Plain.NZB</title>
    <link>https://indexer/download/plain.nzb</link>
    <enclosure url="https://indexer/download/plain.nzb" length="10"/>
    <newznab:attr name="size" value="10"/>
  </item>
  <item>
    <title>Newznab.With.Infohash</title>
    <link>https://indexer/download/hash.nzb</link>
    <newznab:attr name="infohash" value="deadbeef"/>
  </item>
 </channel>
</rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-2", Type: indexer.ProtocolNewznab})
	if c.Type() != indexer.ProtocolNewznab {
		t.Errorf("Type = %q", c.Type())
	}
	rels, err := c.Search(context.Background(), indexer.Query{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("got %d releases, want 2", len(rels))
	}
	if rels[0].Protocol != "nzb" {
		t.Errorf("plain item Protocol = %q, want nzb", rels[0].Protocol)
	}
	if rels[0].Size != 10 {
		t.Errorf("Size = %d, want the size attribute", rels[0].Size)
	}
	if rels[1].Protocol != "torrent" {
		t.Errorf("infohash item Protocol = %q, want torrent", rels[1].Protocol)
	}
	if rels[1].InfoHash != "DEADBEEF" {
		t.Errorf("InfoHash = %q", rels[1].InfoHash)
	}
}

func TestQueryParameters(t *testing.T) {
	var mu sync.Mutex
	var got []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Query())
		mu.Unlock()
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{
		ID:         "idx-1",
		APIKey:     "secret-api-key",
		Categories: []string{"6000", "6010"},
	})

	if _, err := c.Search(context.Background(), indexer.Query{Q: "jane doe", Categories: []string{"7000"}, Limit: 25}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, err := c.Search(context.Background(), indexer.Query{}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2", len(got))
	}
	q := got[0]
	if q.Get("apikey") != "secret-api-key" {
		t.Errorf("apikey = %q", q.Get("apikey"))
	}
	if q.Get("t") != "search" {
		t.Errorf("t = %q", q.Get("t"))
	}
	if q.Get("q") != "jane doe" {
		t.Errorf("q = %q", q.Get("q"))
	}
	if q.Get("cat") != "7000" {
		t.Errorf("cat = %q, want the query override", q.Get("cat"))
	}
	if q.Get("limit") != "25" {
		t.Errorf("limit = %q", q.Get("limit"))
	}
	if q.Get("extended") != "1" {
		t.Errorf("extended = %q", q.Get("extended"))
	}

	def := got[1]
	if def.Get("cat") != "6000,6010" {
		t.Errorf("cat = %q, want the configured categories", def.Get("cat"))
	}
	if def.Get("limit") != "100" {
		t.Errorf("limit = %q, want the default", def.Get("limit"))
	}
	if def.Get("q") != "" {
		t.Errorf("q = %q, want omitted for an empty query", def.Get("q"))
	}
}

func TestErrorElementAtRoot(t *testing.T) {
	const apiKey = "super-secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<error code="100" description="Incorrect user credentials key=` + apiKey + `"/>`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1", APIKey: apiKey})
	_, err := c.Search(context.Background(), indexer.Query{Q: "x"})
	if err == nil {
		t.Fatal("expected an error for an <error> body")
	}
	msg := err.Error()
	if !strings.Contains(msg, "100") {
		t.Errorf("error %q should contain the code", msg)
	}
	if !strings.Contains(msg, "Incorrect user credentials") {
		t.Errorf("error %q should contain the description", msg)
	}
	if strings.Contains(msg, apiKey) {
		t.Errorf("error %q leaks the API key", msg)
	}
	if strings.Contains(msg, "apikey=") {
		t.Errorf("error %q should not contain an unredacted apikey parameter", msg)
	}
}

func TestNon2xxStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal boom"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1", APIKey: "secret"})
	_, err := c.Search(context.Background(), indexer.Query{})
	if err == nil {
		t.Fatal("expected an error for HTTP 500")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") {
		t.Errorf("error %q should contain the status", msg)
	}
	if !strings.Contains(msg, "internal boom") {
		t.Errorf("error %q should contain the body", msg)
	}
	if strings.Contains(msg, "secret") {
		t.Errorf("error %q leaks the API key", msg)
	}
}

func TestMalformedAndEmptyBodies(t *testing.T) {
	for name, body := range map[string]string{
		"empty":             "",
		"garbage":           "<<< not xml",
		"truncated":         `<rss><channel><item><title>x`,
		"unrelated root":    `{"json": true}`,
		"whitespace only":   "   \n\t ",
		"invalid utf8 byte": "\xff\xfe\x00",
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
			if _, err := c.Search(context.Background(), indexer.Query{}); err == nil {
				t.Errorf("Search(%s) = nil error, want a parse error", name)
			}
			if _, err := c.Caps(context.Background()); err == nil {
				t.Errorf("Caps(%s) = nil error, want a parse error", name)
			}
		})
	}
}

func TestNameFallsBackToHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel><item><title>x</title></item></channel></rss>`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-9"})
	rels, err := c.Search(context.Background(), indexer.Query{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d releases, want 1", len(rels))
	}
	if rels[0].Indexer != "127.0.0.1" {
		t.Errorf("Indexer = %q, want the URL host", rels[0].Indexer)
	}
	if rels[0].IndexerID != "idx-9" {
		t.Errorf("IndexerID = %q", rels[0].IndexerID)
	}
}

// --- Fix 1: undefined HTML entities must not fail the whole feed ----------

func TestSearchToleratesUndefinedHTMLEntities(t *testing.T) {
	const body = `<rss><channel>
	  <item>
	    <title>Entity.Release</title>
	    <link>https://indexer/download/entity.torrent</link>
	    <description>Some&nbsp;description with a&nbsp;break</description>
	  </item>
	</channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
	rels, err := c.Search(context.Background(), indexer.Query{})
	if err != nil {
		t.Fatalf("Search with &nbsp; should succeed, got: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d releases, want 1", len(rels))
	}
	if rels[0].DownloadURL != "https://indexer/download/entity.torrent" {
		t.Errorf("DownloadURL = %q", rels[0].DownloadURL)
	}
}

func TestCapsToleratesUndefinedHTMLEntities(t *testing.T) {
	const body = `<caps><categories><category id="6000" name="XXX&nbsp;Content"/></categories></caps>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
	if _, err := c.Caps(context.Background()); err != nil {
		t.Fatalf("Caps with &nbsp; should succeed, got: %v", err)
	}
}

// --- Fix 2: only document-level <error> is an indexer error --------------

func TestFindIndexerErrorDepth(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"root error", `<error code="100" description="bad key"/>`, true},
		{"caps error", `<caps><error code="201" description="bad caps"/></caps>`, true},
		{"rss error", `<rss><error code="300" description="rss"/></rss>`, true},
		{"nested item error", `<rss><channel><item><torrent><error>nested</error></torrent></item></channel></rss>`, false},
		{"valid item", `<rss><channel><item><title>x</title><link>https://x/y.torrent</link></item></channel></rss>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findIndexerError([]byte(tc.body))
			if (got != nil) != tc.want {
				t.Fatalf("findIndexerError(%s) = %v, want error reported = %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestSearchIgnoresNestedItemError(t *testing.T) {
	const body = `<rss><channel>
	  <item>
	    <title>Valid.Release</title>
	    <link>https://indexer/download/valid.torrent</link>
	    <torrent><error>item-level problem, not a feed error</error></torrent>
	  </item>
	</channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
	rels, err := c.Search(context.Background(), indexer.Query{})
	if err != nil {
		t.Fatalf("a nested <error> must not reject the response: %v", err)
	}
	if len(rels) != 1 || rels[0].DownloadURL != "https://indexer/download/valid.torrent" {
		t.Fatalf("got %#v, want the single valid release", rels)
	}
}

func TestSearchKeepsRootErrorDetection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<error code="100" description="bad key"/>`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
	if _, err := c.Search(context.Background(), indexer.Query{}); err == nil {
		t.Fatal("expected a root <error> to be reported")
	}
}

// --- Fix 3: configured userinfo must not leak into errors ----------------

func TestErrorsRedactUserinfoFromConfiguredURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream boom"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	u.User = url.UserPassword("leak-user", "leak-pass")

	c, err := newClient(indexer.Config{ID: "idx-1", URL: u.String()})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	_, err = c.Search(context.Background(), indexer.Query{})
	if err == nil {
		t.Fatal("expected an error for HTTP 500")
	}
	msg := err.Error()
	if strings.Contains(msg, "leak-user") {
		t.Errorf("error %q leaks the username", msg)
	}
	if strings.Contains(msg, "leak-pass") {
		t.Errorf("error %q leaks the password", msg)
	}
}

// --- Fix 4: parsePubDate must accept RFC3339 -----------------------------

func TestParsePubDateLayouts(t *testing.T) {
	cases := map[string]time.Time{
		"2024-10-01T12:34:56Z":            time.Date(2024, 10, 1, 12, 34, 56, 0, time.UTC),
		"Sat, 14 Oct 2023 09:00:00 +0000": time.Date(2023, 10, 14, 9, 0, 0, 0, time.UTC),
		"Wed, 01 Nov 2023 12:30:00 GMT":   time.Date(2023, 11, 1, 12, 30, 0, 0, time.UTC),
		"not a date":                      time.Time{},
	}
	for in, want := range cases {
		if got := parsePubDate(in); !got.Equal(want) {
			t.Errorf("parsePubDate(%q) = %v, want %v", in, got, want)
		}
	}
}

// --- Fix 5: <guid> download URL fallback ---------------------------------

func TestGUIDDownloadURLFallback(t *testing.T) {
	const body = `<rss><channel>
	  <item>
	    <title>Guid.HTTP</title>
	    <guid>https://indexer/download/guid-http.torrent</guid>
	    <link>magnet:?xt=urn:btih:AAAA</link>
	  </item>
	  <item>
	    <title>Guid.URN</title>
	    <guid>urn:btih:BBBB</guid>
	  </item>
	  <item>
	    <title>Guid.Bare</title>
	    <guid>abc123</guid>
	  </item>
	  <item>
	    <title>Enclosure.Wins</title>
	    <guid>https://indexer/download/guid-loses.torrent</guid>
	    <enclosure url="https://indexer/download/enclosure.torrent" length="1"/>
	  </item>
	  <item>
	    <title>Link.Wins</title>
	    <guid>https://indexer/download/guid-loses-too.torrent</guid>
	    <link>https://indexer/download/link.torrent</link>
	  </item>
	</channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "idx-1"})
	rels, err := c.Search(context.Background(), indexer.Query{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []string{
		"https://indexer/download/guid-http.torrent",
		"",
		"",
		"https://indexer/download/enclosure.torrent",
		"https://indexer/download/link.torrent",
	}
	if len(rels) != len(want) {
		t.Fatalf("got %d releases, want %d", len(rels), len(want))
	}
	for i, w := range want {
		if rels[i].DownloadURL != w {
			t.Errorf("release %q DownloadURL = %q, want %q", rels[i].Title, rels[i].DownloadURL, w)
		}
	}
}

// TestRequestsCarryABrowserUserAgent guards a real failure: without an explicit
// User-Agent Go sends "Go-http-client/1.1", which Cloudflare and most Newznab
// indexers answer with a 403 HTML page.
func TestRequestsCarryABrowserUserAgent(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		io.WriteString(w, capsBody)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "i", APIKey: "k", Type: indexer.ProtocolNewznab})
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}

	if gotUA == "" || strings.HasPrefix(gotUA, "Go-http-client") {
		t.Errorf("User-Agent = %q, want a browser-like value", gotUA)
	}
	if !strings.Contains(gotUA, "Mozilla/") {
		t.Errorf("User-Agent = %q, want it to look like a browser", gotUA)
	}
	if !strings.Contains(gotAccept, "xml") {
		t.Errorf("Accept = %q, want it to ask for XML", gotAccept)
	}
}

// TestCloudflareBlockIsExplainedNotDumped covers the error a user actually saw:
// a 403 carrying Cloudflare's "Attention Required!" page. Dumping 512 bytes of
// HTML told them nothing; the message must name the cause and the way out.
func TestCloudflareBlockIsExplainedNotDumped(t *testing.T) {
	const cloudflarePage = `<!DOCTYPE html>
<html class="no-js" lang="en-US"><head><title>Attention Required! | Cloudflare</title></head>
<body><h1>Sorry, you have been blocked</h1></body></html>`

	cases := []struct {
		name        string
		status      int
		contentType string
	}{
		{name: "403 with a content type", status: http.StatusForbidden, contentType: "text/html; charset=UTF-8"},
		{name: "403 with no content type", status: http.StatusForbidden, contentType: ""},
		{name: "200 challenge page", status: http.StatusOK, contentType: "text/html"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, cloudflarePage)
			}))
			defer srv.Close()

			c := newTestClient(t, srv, indexer.Config{ID: "i", APIKey: "secret-key", Type: indexer.ProtocolNewznab})
			err := c.Test(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := err.Error()

			for _, want := range []string{"web page", "Cloudflare", "Prowlarr"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q should mention %q", msg, want)
				}
			}
			if strings.Contains(msg, "<!DOCTYPE") || strings.Contains(msg, "<html") {
				t.Errorf("error should not dump the HTML: %q", msg)
			}
			if strings.Contains(msg, "secret-key") {
				t.Errorf("error leaked the API key: %q", msg)
			}
		})
	}
}

// TestNotFoundSuggestsTheURL covers the second half of the same user report: the
// indexer URL pointed at /api/v2/, which exists nowhere, and the bare "Not
// Found" body gave no clue.
func TestNotFoundSuggestsTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Not Found!"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, indexer.Config{ID: "i", APIKey: "secret-key", Type: indexer.ProtocolNewznab})
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "indexer URL") {
		t.Errorf("error %q should point at the URL", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Errorf("error leaked the API key: %q", err)
	}
}
