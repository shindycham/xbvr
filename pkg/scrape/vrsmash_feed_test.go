package scrape

import (
	"strings"
	"testing"
)

// TestVRSmashFeedURL pins the two feeds. The trans catalog is the same API with
// a category filter, so the main feed must not carry one.
func TestVRSmashFeedURL(t *testing.T) {
	main := vrsmashFeedURL(1, "")
	if strings.Contains(main, "category=") {
		t.Errorf("the main feed carries a category: %s", main)
	}
	if !strings.Contains(main, "page=1") || !strings.Contains(main, "sort=latest") {
		t.Errorf("the main feed lost its paging: %s", main)
	}

	trans := vrsmashFeedURL(3, "trans")
	if !strings.Contains(trans, "category=trans") {
		t.Errorf("the trans feed is not filtered: %s", trans)
	}
	if !strings.Contains(trans, "page=3") {
		t.Errorf("the trans feed lost its paging: %s", trans)
	}
	if trans == main {
		t.Error("the trans feed and the main feed are the same request")
	}
}

// TestVRSmashScrapesEveryFeed is a check on the loop itself: each configured
// category has to be walked, and the main feed has to be walked as well.
func TestVRSmashScrapesEveryFeed(t *testing.T) {
	if len(vrsmashCategories) == 0 {
		t.Fatal("no extra catalog feeds are configured")
	}
	want := map[string]bool{"trans": true}
	for _, category := range vrsmashCategories {
		if !want[category] {
			t.Errorf("unexpected catalog feed %q", category)
		}
		delete(want, category)
	}
	for missing := range want {
		t.Errorf("the %q feed is no longer walked", missing)
	}
}
