package slr

import (
	"testing"
	"time"
)

// Sample from the SexLikeReal page payload (Astro island props, HTML-escaped).
const quotaSample = `...&quot;scriptDownloadLimits&quot;:[0,{&quot;count&quot;:[0,0],&quot;countAi&quot;:[0,0],&quot;limit&quot;:[0,3],&quot;limitAi&quot;:[0,3],&quot;backIn&quot;:[0,1793491200]}],&quot;sceneDownloadLimits&quot;:[0,{&quot;count&quot;:[0,19],&quot;limit&quot;:[0,35],&quot;backIn&quot;:[0,1791849600]}],&quot;isTestUser&quot;:[0,false]...`

func TestExtractQuota(t *testing.T) {
	payload := unescapePayload(quotaSample)

	scene, ok := extractQuota(payload, "sceneDownloadLimits")
	if !ok {
		t.Fatal("sceneDownloadLimits not found")
	}
	if scene.Used != 19 || scene.Limit != 35 || scene.Remaining != 16 {
		t.Errorf("scene quota = %+v, want used=19 limit=35 remaining=16", scene)
	}
	if want := time.Unix(1791849600, 0); !scene.ResetsAt.Equal(want) {
		t.Errorf("scene resets = %v, want %v", scene.ResetsAt, want)
	}

	script, ok := extractQuota(payload, "scriptDownloadLimits")
	if !ok {
		t.Fatal("scriptDownloadLimits not found")
	}
	if script.Used != 0 || script.Limit != 3 || script.Remaining != 3 {
		t.Errorf("script quota = %+v, want used=0 limit=3 remaining=3", script)
	}
}

func TestExtractQuotaMissing(t *testing.T) {
	if _, ok := extractQuota("no limits here", "sceneDownloadLimits"); ok {
		t.Error("expected extractQuota to report not found")
	}
}
