package scrape

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/thoas/go-funk"
	"github.com/tidwall/gjson"

	"github.com/xbapps/xbvr/pkg/models"
	"github.com/xbapps/xbvr/pkg/vrsmash"
)

const vrsmashAPI = "https://www.vrsmash.com/api/content/v1/videos"

// vrsmashCategories are the extra catalog feeds scraped alongside the main one.
//
// They are filtered views of the same catalog, not separate content: the trans
// feed is https://www.vrsmash.com/trans and is served by "category=trans". The
// main feed does contain these scenes, but sparsely - roughly two items in every
// thirty-six - so a scrape that only walks the most recent pages would mostly
// miss them. Walking the feed directly is what makes them reliable.
//
// Adding "gay" here is all that is needed for that catalog (397 scenes).
var vrsmashCategories = []string{"trans"}

// VRSmash scrapes vrsmash.com through its public content API. The catalog is
// paged (`page` + `sort`), and each scene's detail endpoint provides tags,
// description and trailer/paid sources.
// vrsmashFeedURL builds one page of a catalog feed. An empty category is the
// main feed; "trans" is the feed behind https://www.vrsmash.com/trans.
func vrsmashFeedURL(page int, category string) string {
	url := vrsmashAPI + "?page=" + strconv.Itoa(page) + "&sort=latest&limit=36"
	if category != "" {
		url += "&category=" + category
	}
	return url
}

func VRSmash(wg *models.ScrapeWG, updateSite bool, knownScenes []string, out chan<- models.ScrapedScene, singleSceneURL string, scraperID string, siteID string, company string, siteURL string, singeScrapeAdditionalInfo string, limitScraping bool, masterSiteId string) error {
	defer wg.Done()
	logScrapeStart(scraperID, siteID)

	client := resty.New()
	sem := make(chan struct{}, 6)
	apiWG := sync.WaitGroup{}

	// A scene can appear in more than one feed, so it is fetched once per run.
	var seenMu sync.Mutex
	seen := map[string]bool{}
	firstSight := func(slug string) bool {
		seenMu.Lock()
		defer seenMu.Unlock()
		if seen[slug] {
			return false
		}
		seen[slug] = true
		return true
	}

	fetchJSON := func(url string) gjson.Result {
		req := client.R().
			SetHeader("User-Agent", vrsmash.UserAgent).
			SetHeader("Accept", "application/json")
		SetupRestyRequest(GetCoreDomain(url)+"-scraper", req)

		resp, err := req.Get(url)
		if err != nil {
			log.Errorf("vrsmash: request failed for %s: %v", url, err)
			return gjson.Result{}
		}
		if resp.StatusCode() != 200 {
			log.Errorf("vrsmash: HTTP %d for %s", resp.StatusCode(), url)
			return gjson.Result{}
		}
		return gjson.Parse(resp.String())
	}

	processScene := func(slug string, listItem gjson.Result) {
		defer apiWG.Done()

		detail := fetchJSON(vrsmashAPI + "/" + slug)
		item := gjson.Get(detail.Raw, "data.item")
		if !item.Exists() {
			log.Errorf("vrsmash: no detail returned for %s", slug)
			return
		}
		if item.Get("isGame").Bool() {
			return
		}

		homepage := "https://www.vrsmash.com/video/" + slug + "/"

		sc := models.ScrapedScene{}
		sc.ScraperID = scraperID
		sc.SceneType = "VR"
		sc.SiteID = slug
		sc.SceneID = "vrsmash-" + item.Get("id").String()
		sc.HomepageURL = homepage

		sc.Title = strings.TrimSpace(item.Get("name").String())
		sc.Synopsis = strings.TrimSpace(item.Get("description").String())

		if studio := item.Get("studio.name").String(); studio != "" {
			sc.Studio = studio
			sc.Site = studio + " (VRSmash)"
		} else {
			sc.Studio = company
			sc.Site = siteID
		}

		if ts := item.Get("publishedAt").Int(); ts > 0 {
			sc.Released = time.Unix(ts, 0).Format("2006-01-02")
		}
		if secs := item.Get("time").Int(); secs > 0 {
			sc.Duration = int(secs) / 60
		}

		for _, p := range []string{
			item.Get("previewImage.path").String(),
			item.Get("paidThumbnail.path").String(),
			item.Get("freeThumbnail.path").String(),
		} {
			if p != "" && !funk.ContainsString(sc.Covers, p) {
				sc.Covers = append(sc.Covers, p)
			}
		}

		if modelsArr := item.Get("models"); modelsArr.IsArray() {
			sc.ActorDetails = map[string]models.ActorDetails{}
			modelsArr.ForEach(func(_, m gjson.Result) bool {
				name := strings.TrimSpace(m.Get("name").String())
				if name != "" {
					sc.Cast = append(sc.Cast, name)
					if img := m.Get("image.path").String(); img != "" {
						sc.ActorDetails[name] = models.ActorDetails{ImageUrl: img, Source: "vrsmash api"}
					}
				}
				return true
			})
		}

		if cats := item.Get("categories"); cats.IsArray() {
			cats.ForEach(func(_, c gjson.Result) bool {
				name := strings.TrimSpace(c.Get("name").String())
				if name != "" {
					sc.Tags = append(sc.Tags, name)
				}
				return true
			})
		}

		// Trailer sources (free group).
		var trailer models.VideoSourceResponse
		if free := item.Get("sources.free"); free.Exists() {
			free.ForEach(func(_, q gjson.Result) bool {
				path := q.Get("path").String()
				if path != "" {
					quality := q.Get("name").String()
					if quality == "" {
						quality = q.Get("id").String()
					}
					trailer.VideoSources = append(trailer.VideoSources, models.VideoSource{URL: path, Quality: quality})
				}
				return true
			})
		}
		if len(trailer.VideoSources) > 0 {
			sc.TrailerType = "urls"
			if b, err := json.Marshal(trailer); err == nil {
				sc.TrailerSrc = string(b)
			}
		}

		// Filenames so the downloader's output links automatically on rescan.
		for _, q := range vrsmash.FilenameQualities() {
			sc.Filenames = append(sc.Filenames, vrsmash.BuildFilenameFrom(sc.Site, sc.Title, slug, q))
		}

		log.Infof("Scraping [%s] %s", sc.SceneID, sc.Title)
		out <- sc
	}

	if singleSceneURL != "" {
		slug := ""
		if i := strings.Index(singleSceneURL, "/video/"); i != -1 {
			slug = strings.Split(strings.Trim(singleSceneURL[i+len("/video/"):], "/"), "/")[0]
		}
		if slug == "" {
			log.Errorf("vrsmash: could not parse scene slug from %s", singleSceneURL)
		} else {
			apiWG.Add(1)
			processScene(slug, gjson.Result{})
		}
	} else {
		// scrapeFeed walks one catalog feed. An empty category is the main one.
		scrapeFeed := func(category string) {
			page := 1
			for {
				list := fetchJSON(vrsmashFeedURL(page, category))
				items := list.Get("data.items")
				if !items.IsArray() || len(items.Array()) == 0 {
					break
				}

				items.ForEach(func(_, it gjson.Result) bool {
					slug := it.Get("slug").String()
					if slug == "" {
						return true
					}
					homepage := "https://www.vrsmash.com/video/" + slug + "/"
					if funk.ContainsString(knownScenes, homepage) {
						return true
					}
					// Already taken from another feed in this run.
					if !firstSight(slug) {
						return true
					}
					sem <- struct{}{}
					apiWG.Add(1)
					go func(s string, item gjson.Result) {
						defer func() { <-sem }()
						processScene(s, item)
					}(slug, it)
					return true
				})

				totalPages := int(list.Get("data.pages").Int())
				if limitScraping || totalPages == 0 || page >= totalPages {
					break
				}
				page++
				time.Sleep(250 * time.Millisecond)
			}
		}

		// The extra feeds come first. They are the reason to walk them at all
		// when scraping is limited, and going first means a limited scrape spends
		// its pages on them rather than on the main catalog.
		for _, category := range vrsmashCategories {
			scrapeFeed(category)
		}
		scrapeFeed("")
	}

	apiWG.Wait()

	if updateSite {
		updateSiteLastUpdate(scraperID)
	}
	logScrapeFinished(scraperID, siteID)
	return nil
}

func init() {
	registerScraper("vrsmash", "VRSmash", "https://www.vrsmash.com/favicon.ico", "vrsmash.com",
		func(wg *models.ScrapeWG, updateSite bool, knownScenes []string, out chan<- models.ScrapedScene, singleSceneURL string, singeScrapeAdditionalInfo string, limitScraping bool) error {
			return VRSmash(wg, updateSite, knownScenes, out, singleSceneURL, "vrsmash", "VRSmash", "VRSmash", "https://www.vrsmash.com", singeScrapeAdditionalInfo, limitScraping, "")
		})
}
