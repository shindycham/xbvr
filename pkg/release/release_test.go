package release

import (
	"strings"
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestParseName(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		site       string
		siteKey    string
		wantDate   time.Time
		hasDate    bool
		exact      bool
		performers []string
		resolution int
		quality    string
		codec      string
		projection string
		group      string
	}{
		{
			name:       "documented template with container and group",
			raw:        "VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB",
			site:       "VRBangers",
			siteKey:    "vrbangers",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Jane", "Doe"},
			resolution: 2160,
			quality:    "2160p",
			group:      "WRB",
		},
		{
			name:       "180 SBS 8K HEVC",
			raw:        "BadoinkVR.23.05.22.Alex.Coal.XXX.180.SBS.8K.HEVC-GROUP",
			site:       "BadoinkVR",
			siteKey:    "badoinkvr",
			wantDate:   date(2023, time.May, 22),
			hasDate:    true,
			exact:      true,
			performers: []string{"Alex", "Coal"},
			resolution: 4320,
			quality:    "8K",
			codec:      "h265",
			projection: "180_sbs",
			group:      "GROUP",
		},
		{
			name:       "four digit year day first",
			raw:        "CzechVR.12.03.2021.Some.Name.XXX.5K.H265-GROUP",
			site:       "CzechVR",
			siteKey:    "czechvr",
			wantDate:   date(2021, time.March, 12),
			hasDate:    true,
			exact:      true,
			performers: []string{"Some", "Name"},
			resolution: 2700,
			quality:    "5K",
			codec:      "h265",
			group:      "GROUP",
		},
		{
			name:       "mp4 extension is not a group",
			raw:        "NaughtyAmericaVR.15.08.2020.Actor.Name.XXX.4K.mp4",
			site:       "NaughtyAmericaVR",
			siteKey:    "naughtyamericavr",
			wantDate:   date(2020, time.August, 15),
			hasDate:    true,
			exact:      true,
			performers: []string{"Actor", "Name"},
			resolution: 2160,
			quality:    "4K",
		},
		{
			name:       "aggregator site name",
			raw:        "SexLikeReal.20.11.2022.Title.Here.XXX.8K-GROUP",
			site:       "SexLikeReal",
			siteKey:    "sexlikereal",
			wantDate:   date(2022, time.November, 20),
			hasDate:    true,
			exact:      true,
			performers: []string{"Title", "Here"},
			resolution: 4320,
			quality:    "8K",
			group:      "GROUP",
		},
		{
			name:       "lowercase with mkv extension",
			raw:        "virtualrealporn.23.10.14.Actor.XXX.180.sbs.4k.mkv",
			site:       "virtualrealporn",
			siteKey:    "virtualrealporn",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Actor"},
			resolution: 2160,
			quality:    "4k",
			projection: "180_sbs",
		},
		{
			name:       "3dh 180x180 marker",
			raw:        "WankzVR.21.02.19.Actor.Name.XXX.180x180.3dh.5K-GROUP",
			site:       "WankzVR",
			siteKey:    "wankzvr",
			wantDate:   date(2021, time.February, 19),
			hasDate:    true,
			exact:      true,
			performers: []string{"Actor", "Name"},
			resolution: 2700,
			quality:    "5K",
			group:      "GROUP",
		},
		{
			name:       "dash separated date",
			raw:        "VRBangers.2023-10-14.Jane.Doe.XXX.2160p",
			site:       "VRBangers",
			siteKey:    "vrbangers",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Jane", "Doe"},
			resolution: 2160,
			quality:    "2160p",
		},
		{
			name:       "compact date token",
			raw:        "VRBangers.20231014.Jane.Doe.XXX.2160p",
			site:       "VRBangers",
			siteKey:    "vrbangers",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Jane", "Doe"},
			resolution: 2160,
			quality:    "2160p",
		},
		{
			name:       "month first fallback when day first is impossible",
			raw:        "SomeSite.10.14.23.Actor.XXX.2160p-GRP",
			site:       "SomeSite",
			siteKey:    "somesite",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Actor"},
			resolution: 2160,
			quality:    "2160p",
			group:      "GRP",
		},
		{
			name:       "bracketed group prefix",
			raw:        "[WRB]VRBangers.23.10.14.Jane.Doe.XXX.2160p",
			site:       "VRBangers",
			siteKey:    "vrbangers",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Jane", "Doe"},
			resolution: 2160,
			quality:    "2160p",
			group:      "WRB",
		},
		{
			name:       "hyphenated performer without markers keeps both halves",
			raw:        "VRBangers.23.10.14.Jane-Doe",
			site:       "VRBangers",
			siteKey:    "vrbangers",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Jane", "Doe"},
		},
		{
			name:       "hyphenated performer keeps both halves even with markers",
			raw:        "VRBangers.23.10.14.Jane-Doe.XXX.2160p",
			site:       "VRBangers",
			siteKey:    "vrbangers",
			wantDate:   date(2023, time.October, 14),
			hasDate:    true,
			exact:      true,
			performers: []string{"Jane", "Doe"},
			resolution: 2160,
			quality:    "2160p",
		},
		{
			name:       "combined performer list drops the conjunction",
			raw:        "POVR.23.01.01.Jane.Doe.And.John.Smith.XXX.6K-GRP",
			site:       "POVR",
			siteKey:    "povr",
			wantDate:   date(2023, time.January, 1),
			hasDate:    true,
			exact:      true,
			performers: []string{"Jane", "Doe", "John", "Smith"},
			resolution: 3240,
			quality:    "6K",
			group:      "GRP",
		},
		{
			name:       "year only",
			raw:        "VRHush.2023.Actor.Name.XXX.2160p",
			site:       "VRHush",
			siteKey:    "vrhush",
			wantDate:   date(2023, time.January, 1),
			hasDate:    true,
			exact:      false,
			performers: []string{"Actor", "Name"},
			resolution: 2160,
			quality:    "2160p",
		},
		{
			name:    "empty",
			raw:     "",
			siteKey: "",
		},
		{
			name:       "not a release name at all",
			raw:        "some random file.mkv",
			site:       "some",
			siteKey:    "some",
			performers: []string{"random", "file"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseName(tt.raw)

			if got.Raw != tt.raw {
				t.Errorf("Raw = %q, want %q", got.Raw, tt.raw)
			}
			if got.Site != tt.site {
				t.Errorf("Site = %q, want %q", got.Site, tt.site)
			}
			if got.SiteKey != tt.siteKey {
				t.Errorf("SiteKey = %q, want %q", got.SiteKey, tt.siteKey)
			}
			if got.HasDate != tt.hasDate {
				t.Fatalf("HasDate = %v, want %v", got.HasDate, tt.hasDate)
			}
			if tt.hasDate {
				if !got.Date.Equal(tt.wantDate) {
					t.Errorf("Date = %s, want %s", got.Date.Format("2006-01-02"), tt.wantDate.Format("2006-01-02"))
				}
				if got.DateIsExact != tt.exact {
					t.Errorf("DateIsExact = %v, want %v", got.DateIsExact, tt.exact)
				}
			}
			if strings.Join(got.Performers, " ") != strings.Join(tt.performers, " ") {
				t.Errorf("Performers = %v, want %v", got.Performers, tt.performers)
			}
			if got.Resolution != tt.resolution {
				t.Errorf("Resolution = %d, want %d", got.Resolution, tt.resolution)
			}
			if got.Quality != tt.quality {
				t.Errorf("Quality = %q, want %q", got.Quality, tt.quality)
			}
			if got.Codec != tt.codec {
				t.Errorf("Codec = %q, want %q", got.Codec, tt.codec)
			}
			if got.Projection != tt.projection {
				t.Errorf("Projection = %q, want %q", got.Projection, tt.projection)
			}
			if got.Group != tt.group {
				t.Errorf("Group = %q, want %q", got.Group, tt.group)
			}
		})
	}
}

func TestParseNameNeverPanics(t *testing.T) {
	inputs := []string{
		"", ".", "..", "-", "--", "[]", "[", "]", "....", "a", "-a", "a-",
		"2023.10.14", "14.10.2023", "00.00.0000", "99.99.99",
		"[WRB]", "[WRB]x", "x[WRB]", "a-b-c-d", "1.2.3.4.5.6.7",
		"vrsmash-14d3222a-1a2b-3c4d-5e6f-7a8b9c0d1e2f",
		"8f2f0e0e-1234-5678-9abc-def012345678.mp4",
		"../../etc/passwd",
		"VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB.mp4.part",
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ParseName(%q) panicked: %v", in, r)
				}
			}()
			_ = ParseName(in)
		}()
	}
}

func TestParseFilename(t *testing.T) {
	got := ParseFilename("/downloads/torrents/VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB/VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB.mp4")
	if got.SiteKey != "vrbangers" {
		t.Errorf("SiteKey = %q, want %q", got.SiteKey, "vrbangers")
	}
	if !got.Date.Equal(date(2023, time.October, 14)) {
		t.Errorf("Date = %s, want 2023-10-14", got.Date.Format("2006-01-02"))
	}
	if got.Group != "WRB" {
		t.Errorf("Group = %q, want %q", got.Group, "WRB")
	}
}

func TestNormalizeSiteKey(t *testing.T) {
	tests := map[string]string{
		"VRBangers":         "vrbangers",
		"NaughtyAmericaVR":  "naughtyamericavr",
		"SexLikeReal":       "sexlikereal",
		"BadoinkVR":         "badoinkvr",
		"virtual-real-porn": "virtualrealporn",
		"  CzechVR  ":       "czechvr",
		"TMW VR Net":        "tmwvrnet",
	}
	for in, want := range tests {
		if got := NormalizeSiteKey(in); got != want {
			t.Errorf("NormalizeSiteKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolutionOf(t *testing.T) {
	tests := []struct {
		token string
		want  int
		ok    bool
	}{
		{"2160p", 2160, true},
		{"1080p", 1080, true},
		{"720p", 720, true},
		{"4k", 2160, true},
		{"5k", 2700, true},
		{"6k", 3240, true},
		{"8k", 4320, true},
		{"2k", 1080, true},
		{"1k", 0, false},
		{"k", 0, false},
		{"1080", 0, false},
		{"p", 0, false},
	}
	for _, tt := range tests {
		got, ok := resolutionOf(tt.token)
		if got != tt.want || ok != tt.ok {
			t.Errorf("resolutionOf(%q) = (%d, %v), want (%d, %v)", tt.token, got, ok, tt.want, tt.ok)
		}
	}
}

// TestParseNameStripsIndexerExtensions covers names that arrive with a Usenet or
// torrent extension attached, which must not swallow the release group.
func TestParseNameStripsIndexerExtensions(t *testing.T) {
	for _, name := range []string{
		"VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB.nzb",
		"VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB.torrent",
	} {
		got := ParseName(name)
		if got.Group != "WRB" {
			t.Errorf("ParseName(%q).Group = %q, want WRB", name, got.Group)
		}
		if got.SiteKey != "vrbangers" || !got.Date.Equal(date(2023, time.October, 14)) {
			t.Errorf("ParseName(%q) = site %q date %s", name, got.SiteKey, got.Date.Format("2006-01-02"))
		}
		if strings.Join(got.Performers, " ") != "Jane Doe" {
			t.Errorf("ParseName(%q).Performers = %v, want [Jane Doe]", name, got.Performers)
		}
	}
}
