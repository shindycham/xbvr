package slr

import (
	"testing"
	"time"
)

// withFakeFetch replaces the network call with a counter and restores it after.
func withFakeFetch(t *testing.T, info QuotaInfo) *int {
	t.Helper()
	calls := new(int)
	saved := fetchQuota
	fetchQuota = func() QuotaInfo {
		*calls++
		return info
	}
	t.Cleanup(func() {
		fetchQuota = saved
		InvalidateQuota()
	})
	InvalidateQuota()
	return calls
}

// TestFetchQuotaCachesAndInvalidates covers the bug where the Downloads view
// showed a stale allowance for minutes after a download: nothing ever dropped
// the cache, so a finished download was not reflected until the TTL expired.
func TestFetchQuotaCachesAndInvalidates(t *testing.T) {
	info := QuotaInfo{Scene: Quota{Used: 19, Limit: 35, Remaining: 16}, FetchedAt: time.Now()}
	calls := withFakeFetch(t, info)

	// The first call fetches; the ones after it are served from the cache, which
	// is the whole point of having one.
	for i := 0; i < 5; i++ {
		got := FetchQuota(false)
		if got.Scene.Remaining != 16 {
			t.Fatalf("call %d returned remaining %d, want 16", i, got.Scene.Remaining)
		}
	}
	if *calls != 1 {
		t.Fatalf("fetched %d times for five reads, want 1", *calls)
	}

	// A download finishing drops the cache, so the next read must go and look.
	InvalidateQuota()
	if got := FetchQuota(false); got.Scene.Remaining != 16 {
		t.Errorf("remaining = %d after invalidation", got.Scene.Remaining)
	}
	if *calls != 2 {
		t.Errorf("fetched %d times, want 2: invalidation did not force a refetch", *calls)
	}
}

// TestFetchQuotaForceBypassesTheCache guards the manual Refresh button.
func TestFetchQuotaForceBypassesTheCache(t *testing.T) {
	info := QuotaInfo{Scene: Quota{Used: 1, Limit: 35, Remaining: 34}, FetchedAt: time.Now()}
	calls := withFakeFetch(t, info)

	FetchQuota(false)
	FetchQuota(false)
	if *calls != 1 {
		t.Fatalf("fetched %d times, want 1", *calls)
	}
	FetchQuota(true)
	if *calls != 2 {
		t.Errorf("force did not bypass the cache: %d fetches", *calls)
	}
}

// TestFetchQuotaKeepsTheLastGoodValueWhenARefreshFails checks that a transient
// failure does not blank the number on screen.
func TestFetchQuotaKeepsTheLastGoodValueWhenARefreshFails(t *testing.T) {
	good := QuotaInfo{Scene: Quota{Used: 19, Limit: 35, Remaining: 16}, FetchedAt: time.Now()}
	calls := withFakeFetch(t, good)

	if got := FetchQuota(false); got.Scene.Remaining != 16 {
		t.Fatalf("remaining = %d, want 16", got.Scene.Remaining)
	}

	// Now make the fetch fail and force a read.
	fetchQuota = func() QuotaInfo {
		*calls++
		return QuotaInfo{FetchedAt: time.Now(), Error: "session expired"}
	}
	got := FetchQuota(true)
	if got.Scene.Remaining != 16 {
		t.Errorf("remaining = %d, want the last known value 16", got.Scene.Remaining)
	}
	if got.Error == "" {
		t.Error("the failure should still be reported alongside the stale value")
	}
}
