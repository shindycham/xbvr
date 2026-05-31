package scrape

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/models"
)

const (
	vrsmashDomain  = "vrsmash.com"
	vrsmashBaseURL = "https://www.vrsmash.com"
)

func VRSmash(wg *models.ScrapeWG, updateSite bool, knownScenes []string, out chan<- models.ScrapedScene, singleSceneURL string, scraperID string, siteID string, company string, siteURL string, singeScrapeAdditionalInfo string, limitScraping bool, masterSiteId string) error {
	defer wg.Done()
	logScrapeStart(scraperID, siteID)

	allowedDomains := []string{vrsmashDomain, "www." + vrsmashDomain}
	sceneCollector := createCollector(allowedDomains...)
	siteCollector := createCollector(allowedDomains...)

	sceneCollector.OnHTML(`html`, func(e *colly.HTMLElement) {
		videoSchema := vrsmashVideoSchema(e)
		slug := vrsmashSlugFromURL(e.Request.URL.String())
		if slug == "" {
			log.Infof("Unable to determine a Scene Id for %s", e.Request.URL)
			return
		}

		studio := strings.TrimSpace(company)
		if studio == "" {
			studio = strings.TrimSpace(vrsmashString(vrsmashMap(videoSchema, "producer"), "name"))
		}
		if studio == "" {
			studio = strings.TrimSpace(e.ChildText(`.ui-detail-video__title`))
		}

		sc := models.ScrapedScene{}
		sc.SiteID = slug
		sc.SceneID = "vrsmash-" + sc.SiteID
		sc.ScraperID = scraperID
		sc.SceneType = "VR"
		sc.Studio = studio
		sc.Site = siteID
		sc.HomepageURL = vrsmashBaseURL + "/video/" + slug + "/"
		sc.MasterSiteId = masterSiteId

		sc.Title = strings.TrimSpace(vrsmashString(videoSchema, "name"))
		if sc.Title == "" {
			sc.Title = strings.TrimSpace(e.ChildText(`h1`))
		}
		if sc.Title == "" {
			log.Infof("Unable to determine a title for %s", e.Request.URL)
			return
		}

		cover := vrsmashString(videoSchema, "thumbnailUrl")
		if cover == "" {
			cover = e.ChildAttr(`meta[property="og:image"]`, "content")
		}
		if cover != "" {
			sc.Covers = append(sc.Covers, cover)
		}

		sc.Synopsis = strings.TrimSpace(e.ChildText(`.ui-detail-video .description .text`))
		if sc.Synopsis == "" {
			sc.Synopsis = strings.TrimSpace(vrsmashString(videoSchema, "description"))
		}

		sc.Released = vrsmashDate(vrsmashString(videoSchema, "uploadDate"))
		sc.Duration = vrsmashDurationMinutes(vrsmashString(videoSchema, "duration"))

		sc.Tags = vrsmashTags(e, videoSchema)
		sc.ActorDetails = make(map[string]models.ActorDetails)
		vrsmashCast(e, videoSchema, &sc)

		sc.TrailerType = "scrape_html"
		params := models.TrailerScrape{SceneUrl: sc.HomepageURL, HtmlElement: `meta[property="og:video"]`, ContentPath: "content"}
		strParams, _ := json.Marshal(params)
		sc.TrailerSrc = string(strParams)

		out <- sc
	})

	seenSceneURLs := make(map[string]bool)
	siteCollector.OnHTML(`article.ui-video-card a[href^="/video/"]`, func(e *colly.HTMLElement) {
		sceneURL := e.Request.AbsoluteURL(e.Attr("href"))
		if sceneURL == "" || seenSceneURLs[sceneURL] {
			return
		}
		seenSceneURLs[sceneURL] = true
		if !vrsmashKnownScene(knownScenes, sceneURL) {
			sceneCollector.Visit(sceneURL)
		}
	})

	siteCollector.OnHTML(`a[href]`, func(e *colly.HTMLElement) {
		if limitScraping || !strings.Contains(strings.TrimSpace(e.Text), "Next page") {
			return
		}
		pageURL := e.Request.AbsoluteURL(e.Attr("href"))
		if pageURL != "" {
			WaitBeforeVisit(vrsmashDomain, siteCollector.Visit, pageURL)
		}
	})

	if singleSceneURL != "" {
		sceneCollector.Visit(singleSceneURL)
	} else {
		if siteURL == "" {
			siteURL = vrsmashBaseURL + "/all/"
		}
		WaitBeforeVisit(vrsmashDomain, siteCollector.Visit, siteURL)
	}

	if updateSite {
		updateSiteLastUpdate(scraperID)
	}
	logScrapeFinished(scraperID, siteID)
	return nil
}

func addVRSmashScraper(id string, name string, company string, avatarURL string, custom bool, siteURL string, masterSiteId string) {
	suffixedName := name
	siteNameSuffix := name
	if custom {
		suffixedName += " (Custom VRSmash)"
		siteNameSuffix += " (VRSmash)"
	} else {
		suffixedName += " (VRSmash)"
	}
	if avatarURL == "" {
		avatarURL = vrsmashBaseURL + "/favicon.ico"
	}

	if masterSiteId == "" {
		registerScraper(id, suffixedName, avatarURL, vrsmashDomain, func(wg *models.ScrapeWG, updateSite bool, knownScenes []string, out chan<- models.ScrapedScene, singleSceneURL string, singeScrapeAdditionalInfo string, limitScraping bool) error {
			return VRSmash(wg, updateSite, knownScenes, out, singleSceneURL, id, siteNameSuffix, company, siteURL, singeScrapeAdditionalInfo, limitScraping, "")
		})
	} else {
		registerAlternateScraper(id, suffixedName, avatarURL, vrsmashDomain, masterSiteId, func(wg *models.ScrapeWG, updateSite bool, knownScenes []string, out chan<- models.ScrapedScene, singleSceneURL string, singeScrapeAdditionalInfo string, limitScraping bool) error {
			return VRSmash(wg, updateSite, knownScenes, out, singleSceneURL, id, siteNameSuffix, company, siteURL, singeScrapeAdditionalInfo, limitScraping, masterSiteId)
		})
	}
}

func init() {
	registerScraper("vrsmash-single_scene", "VRSmash - Other Studios", vrsmashBaseURL+"/favicon.ico", vrsmashDomain, func(wg *models.ScrapeWG, updateSite bool, knownScenes []string, out chan<- models.ScrapedScene, singleSceneURL string, singeScrapeAdditionalInfo string, limitScraping bool) error {
		return VRSmash(wg, updateSite, knownScenes, out, singleSceneURL, "", "", "", "", singeScrapeAdditionalInfo, limitScraping, "")
	})
	registerScraper("vrsmash", "VRSmash", vrsmashBaseURL+"/favicon.ico", vrsmashDomain, func(wg *models.ScrapeWG, updateSite bool, knownScenes []string, out chan<- models.ScrapedScene, singleSceneURL string, singeScrapeAdditionalInfo string, limitScraping bool) error {
		return VRSmash(wg, updateSite, knownScenes, out, singleSceneURL, "vrsmash", "VRSmash", "", vrsmashBaseURL+"/all/", singeScrapeAdditionalInfo, limitScraping, "")
	})

	var scrapers config.ScraperList
	scrapers.Load()
	for _, scraper := range scrapers.XbvrScrapers.VrsmashScrapers {
		addVRSmashScraper(scraper.ID, scraper.Name, scraper.Company, scraper.AvatarUrl, false, scraper.URL, scraper.MasterSiteId)
	}
	for _, scraper := range scrapers.CustomScrapers.VrsmashScrapers {
		addVRSmashScraper(scraper.ID, scraper.Name, scraper.Company, scraper.AvatarUrl, true, scraper.URL, scraper.MasterSiteId)
	}
}

func vrsmashVideoSchema(e *colly.HTMLElement) map[string]interface{} {
	var video map[string]interface{}
	e.ForEach(`script[type="application/ld+json"]`, func(id int, script *colly.HTMLElement) {
		if video != nil {
			return
		}
		var root map[string]interface{}
		if err := json.Unmarshal([]byte(script.Text), &root); err != nil {
			return
		}
		video = vrsmashFindVideoSchema(root)
	})
	if video == nil {
		return map[string]interface{}{}
	}
	return video
}

func vrsmashFindVideoSchema(root map[string]interface{}) map[string]interface{} {
	if vrsmashString(root, "@type") == "VideoObject" {
		return root
	}
	if mainEntity := vrsmashMap(root, "mainEntity"); vrsmashString(mainEntity, "@type") == "VideoObject" {
		return mainEntity
	}
	for _, item := range vrsmashArray(root, "@graph") {
		node, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if vrsmashString(node, "@type") == "VideoObject" {
			return node
		}
		if mainEntity := vrsmashMap(node, "mainEntity"); vrsmashString(mainEntity, "@type") == "VideoObject" {
			return mainEntity
		}
	}
	return nil
}

func vrsmashString(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	switch v := data[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

func vrsmashMap(data map[string]interface{}, key string) map[string]interface{} {
	if data == nil {
		return nil
	}
	value, _ := data[key].(map[string]interface{})
	return value
}

func vrsmashArray(data map[string]interface{}, key string) []interface{} {
	if data == nil {
		return nil
	}
	switch value := data[key].(type) {
	case []interface{}:
		return value
	case map[string]interface{}:
		return []interface{}{value}
	case string:
		return []interface{}{value}
	default:
		return nil
	}
}

func vrsmashTags(e *colly.HTMLElement, videoSchema map[string]interface{}) []string {
	skipTags := map[string]bool{
		"180": true,
		"360": true,
		"3D":  true,
		"HD":  true,
		"VR":  true,
	}
	tags := []string{}
	seen := make(map[string]bool)
	addTag := func(tag string) {
		tag = strings.TrimSpace(tag)
		if tag == "" || skipTags[tag] || seen[strings.ToLower(tag)] {
			return
		}
		seen[strings.ToLower(tag)] = true
		tags = append(tags, tag)
	}

	e.ForEach(`.ui-detail-video .tags a`, func(id int, tag *colly.HTMLElement) {
		addTag(tag.Text)
	})
	if len(tags) == 0 {
		for _, rawTag := range strings.Split(vrsmashString(videoSchema, "genre"), ",") {
			addTag(rawTag)
		}
		for _, item := range vrsmashArray(videoSchema, "isPartOf") {
			if tag, ok := item.(map[string]interface{}); ok {
				addTag(vrsmashString(tag, "name"))
			}
		}
	}
	return tags
}

func vrsmashCast(e *colly.HTMLElement, videoSchema map[string]interface{}, sc *models.ScrapedScene) {
	actorProfileByName := make(map[string]string)
	for _, item := range vrsmashArray(videoSchema, "actor") {
		actor, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name := strings.TrimSpace(vrsmashString(actor, "name"))
		if name == "" {
			continue
		}
		profileURL := vrsmashString(actor, "url")
		sc.Cast = append(sc.Cast, name)
		actorProfileByName[name] = profileURL
		sc.ActorDetails[name] = models.ActorDetails{Source: "vrsmash scrape", ProfileUrl: profileURL}
	}

	e.ForEach(`article.ui-card-model`, func(id int, actorCard *colly.HTMLElement) {
		name := strings.TrimSpace(actorCard.ChildText(`.ui-card-model__name`))
		if name == "" {
			return
		}
		if _, ok := actorProfileByName[name]; !ok {
			sc.Cast = append(sc.Cast, name)
		}
		profileURL := actorCard.Request.AbsoluteURL(actorCard.ChildAttr(`a[href^="/pornstars/"]`, "href"))
		imageURL := vrsmashCleanImageURL(actorCard.ChildAttr(`img`, "src"))
		sc.ActorDetails[name] = models.ActorDetails{Source: "vrsmash scrape", ImageUrl: imageURL, ProfileUrl: profileURL}
	})
}

func vrsmashCleanImageURL(imageURL string) string {
	if idx := strings.Index(imageURL, "https://cdn-pub.vrsmash.com/"); idx >= 0 {
		return imageURL[idx:]
	}
	return imageURL
}

func vrsmashDurationMinutes(duration string) int {
	if duration == "" {
		return 0
	}
	re := regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)
	m := re.FindStringSubmatch(duration)
	if len(m) != 4 {
		return 0
	}
	hours, _ := strconv.Atoi("0" + m[1])
	minutes, _ := strconv.Atoi("0" + m[2])
	seconds, _ := strconv.Atoi("0" + m[3])
	return (hours*3600 + minutes*60 + seconds) / 60
}

func vrsmashDate(date string) string {
	if date == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, date)
	if err == nil {
		return t.Format("2006-01-02")
	}
	if idx := strings.Index(date, "T"); idx > 0 {
		return date[:idx]
	}
	return date
}

func vrsmashSlugFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.Trim(strings.TrimSuffix(path.Base(rawURL), "/"), "/")
	}
	return strings.Trim(strings.TrimSuffix(path.Base(strings.TrimSuffix(parsed.Path, "/")), "/"), "/")
}

func vrsmashKnownScene(knownScenes []string, sceneURL string) bool {
	for _, known := range knownScenes {
		if strings.TrimRight(known, "/") == strings.TrimRight(sceneURL, "/") {
			return true
		}
	}
	return false
}
