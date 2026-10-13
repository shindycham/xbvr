package vrsmash

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbapps/xbvr/pkg/models"
)

// withTestServer points the provider at a local HTTP server and a fixed cookie
// jar, so the full request/parse path can be tested without a live session.
func withTestServer(t *testing.T, handler http.HandlerFunc, fn func()) {
	t.Helper()

	srv := httptest.NewServer(handler)
	defer srv.Close()

	oldBase, oldJar := apiBase, jarFn
	apiBase = srv.URL
	jarFn = func() (map[string]string, error) {
		return map[string]string{"atoken": "test-token", "cf_clearance": "x"}, nil
	}
	defer func() {
		apiBase = oldBase
		jarFn = oldJar
	}()

	fn()
}

func testScene() models.Scene {
	return models.Scene{SceneID: "vrsmash-test", SceneURL: "https://www.vrsmash.com/video/x/"}
}

func TestGetRenditionsPaidSources(t *testing.T) {
	body := `{"status":{"code":1},"data":{"item":{"sources":{` +
		`"free":{"4k":{"name":"4k","path":"https://cdn/trailer4k.mp4"}},` +
		`"paid":{"6k":{"name":"6k","path":"https://cdn/full6k.mp4","size":123},` +
		`"8k":{"name":"8k","path":"https://cdn/full8k.mp4","size":456}}}}}}`

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if !strings.Contains(r.Header.Get("Cookie"), "cf_clearance=x") {
			t.Errorf("expected the cookie jar to be sent, got %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}, func() {
		rs, err := GetRenditions(testScene())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rs) != 2 {
			t.Fatalf("expected the 2 paid renditions, got %d: %+v", len(rs), rs)
		}
		if rs[0].Quality != "6K" || rs[1].Quality != "8K" {
			t.Errorf("expected ascending 6K/8K, got %+v", rs)
		}
		if rs[0].Size != 123 || rs[0].URL != "https://cdn/full6k.mp4" {
			t.Errorf("paid source not parsed: %+v", rs[0])
		}
		if rs[0].Height != 3240 {
			t.Errorf("expected 6K -> 3240, got %d", rs[0].Height)
		}
	})
}

func TestGetRenditionsFreeOnlyIsTrailerError(t *testing.T) {
	body := `{"data":{"item":{"sources":{"free":{"4k":{"name":"4k","path":"https://cdn/trailer4k.mp4"}}}}}}`

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}, func() {
		_, err := GetRenditions(testScene())
		if err == nil || !strings.Contains(err.Error(), "trailer") {
			t.Fatalf("expected a trailer/authorisation error, got %v", err)
		}
	})
}

func TestGetRenditionsAltPaidGroupName(t *testing.T) {
	body := `{"data":{"item":{"sources":{` +
		`"free":{"4k":{"name":"4k","path":"https://cdn/trailer4k.mp4"}},` +
		`"premium":{"6k":{"name":"6k","path":"https://cdn/full6k.mp4","size":10}}}}}}`

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}, func() {
		rs, err := GetRenditions(testScene())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rs) != 1 || rs[0].Quality != "6K" {
			t.Fatalf("expected the premium group, got %+v", rs)
		}
	})
}

func TestGetRenditionsUnauthorized(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, func() {
		_, err := GetRenditions(testScene())
		if err == nil || !strings.Contains(err.Error(), "session expired") {
			t.Fatalf("expected a session expired error, got %v", err)
		}
	})
}

func TestGetRenditionsForbidden(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}, func() {
		_, err := GetRenditions(testScene())
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("expected a blocked error, got %v", err)
		}
	})
}
