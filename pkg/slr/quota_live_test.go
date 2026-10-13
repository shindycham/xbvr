package slr

import (
	"os"
	"testing"
)

// Live check: set XBVR_SLR_LIVE=1 to fetch the real quota using the machine's
// SexLikeReal session (Firefox profile or collector config).
func TestFetchQuotaLive(t *testing.T) {
	if os.Getenv("XBVR_SLR_LIVE") != "1" {
		t.Skip("set XBVR_SLR_LIVE=1 to run the live quota check")
	}

	q := FetchQuota(true)
	t.Logf("scene : used=%d limit=%d remaining=%d resets=%v", q.Scene.Used, q.Scene.Limit, q.Scene.Remaining, q.Scene.ResetsAt)
	t.Logf("script: used=%d limit=%d remaining=%d resets=%v", q.Script.Used, q.Script.Limit, q.Script.Remaining, q.Script.ResetsAt)

	if q.Error != "" {
		t.Fatalf("quota error: %s", q.Error)
	}
	if q.Scene.Limit == 0 {
		t.Fatalf("no scene download limit parsed: %+v", q.Scene)
	}
}
