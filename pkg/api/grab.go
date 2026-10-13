package api

import (
	"net/http"
	"strconv"

	restfulspec "github.com/emicklei/go-restful-openapi/v2"
	"github.com/emicklei/go-restful/v3"

	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/models"
	"github.com/xbapps/xbvr/pkg/tasks"
)

type RequestGrabStart struct {
	SceneID   uint                   `json:"scene_id"`
	Candidate tasks.ReleaseCandidate `json:"candidate"`
}

type RequestGrabID struct {
	ID string `json:"id"`
}

type ResponseGrabSearch struct {
	SceneID   string `json:"scene_id"`
	SceneDBID uint   `json:"scene_db_id"`
	Title     string `json:"title"`
	// Query is the caller's override, if any; Queries is what was searched.
	Query      string                   `json:"query"`
	Queries    []string                 `json:"queries"`
	Protocol   string                   `json:"protocol"`
	Candidates []tasks.ReleaseCandidate `json:"candidates"`
}

// GrabResource exposes torrent and NZB grabbing: searching indexers for a
// scene's releases, handing a chosen one to an external download client, and
// tracking it until it lands in the library.
type GrabResource struct{}

func (i GrabResource) WebService() *restful.WebService {
	tags := []string{"Downloads"}

	ws := new(restful.WebService)
	ws.Path("/api/grab").
		Consumes(restful.MIME_JSON).
		Produces(restful.MIME_JSON)

	ws.Route(ws.GET("/search").To(i.search).
		Param(ws.QueryParameter("scene-id", "Internal scene ID").DataType("int").Required(true)).
		Param(ws.QueryParameter("protocol", "torrent, nzb, or empty for both")).
		Param(ws.QueryParameter("q", "Override the generated search string")).
		Metadata(restfulspec.KeyOpenAPITags, tags).
		Writes(ResponseGrabSearch{}))

	ws.Route(ws.POST("/start").To(i.start).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.GET("/list").To(i.list).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/cancel").To(i.cancel).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/remove").To(i.remove).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/refresh").To(i.refresh).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/test-indexer").To(i.testIndexer).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/test-client").To(i.testClient).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.GET("/status").To(i.status).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	return ws
}

func (i GrabResource) search(req *restful.Request, resp *restful.Response) {
	id, err := strconv.Atoi(req.QueryParameter("scene-id"))
	if err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}

	var scene models.Scene
	if err := scene.GetIfExistByPK(uint(id)); err != nil {
		APIError(req, resp, http.StatusNotFound, err)
		return
	}

	protocol := req.QueryParameter("protocol")
	query := req.QueryParameter("q")

	candidates, err := tasks.SearchSceneReleases(scene, protocol, query)
	if err != nil {
		// A failed search is not a server error: it is usually a missing indexer
		// or a bad API key, and the message is what the user needs to see.
		resp.WriteHeaderAndEntity(http.StatusOK, map[string]interface{}{
			"error":      err.Error(),
			"candidates": []tasks.ReleaseCandidate{},
		})
		return
	}
	if candidates == nil {
		candidates = []tasks.ReleaseCandidate{}
	}

	resp.WriteHeaderAndEntity(http.StatusOK, ResponseGrabSearch{
		SceneID:    scene.SceneID,
		SceneDBID:  scene.ID,
		Title:      scene.Title,
		Query:      query,
		Queries:    tasks.GrabSearchQueries(scene, query),
		Protocol:   protocol,
		Candidates: candidates,
	})
}

func (i GrabResource) start(req *restful.Request, resp *restful.Response) {
	var r RequestGrabStart
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}

	var scene models.Scene
	if err := scene.GetIfExistByPK(r.SceneID); err != nil {
		APIError(req, resp, http.StatusNotFound, err)
		return
	}

	grab, err := tasks.StartSceneGrab(scene, r.Candidate)
	if err != nil {
		resp.WriteHeaderAndEntity(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"grab":  grab,
		})
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, grab)
}

func (i GrabResource) list(req *restful.Request, resp *restful.Response) {
	grabs := tasks.GetGrabs()
	if grabs == nil {
		grabs = []tasks.Grab{}
	}
	resp.WriteHeaderAndEntity(http.StatusOK, grabs)
}

func (i GrabResource) cancel(req *restful.Request, resp *restful.Response) {
	var r RequestGrabID
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	if err := tasks.CancelGrab(r.ID); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, map[string]string{"status": "cancelled"})
}

func (i GrabResource) remove(req *restful.Request, resp *restful.Response) {
	var r RequestGrabID
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	tasks.RemoveGrab(r.ID)
	resp.WriteHeaderAndEntity(http.StatusOK, map[string]string{"status": "removed"})
}

// refresh forces a poll of every download client, so the UI can update without
// waiting for the next tick.
func (i GrabResource) refresh(req *restful.Request, resp *restful.Response) {
	tasks.PollGrabs()
	grabs := tasks.GetGrabs()
	if grabs == nil {
		grabs = []tasks.Grab{}
	}
	resp.WriteHeaderAndEntity(http.StatusOK, grabs)
}

func (i GrabResource) testIndexer(req *restful.Request, resp *restful.Response) {
	var r config.IndexerConfig
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	if err := tasks.TestIndexerConfig(r); err != nil {
		resp.WriteHeaderAndEntity(http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, map[string]interface{}{"ok": true})
}

func (i GrabResource) testClient(req *restful.Request, resp *restful.Response) {
	var r config.DownloadClientConfig
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	if err := tasks.TestDownloadClientConfig(r); err != nil {
		resp.WriteHeaderAndEntity(http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, map[string]interface{}{"ok": true})
}

type ResponseGrabStatus struct {
	Enabled             bool   `json:"enabled"`
	ImportDir           string `json:"import_dir"`
	ImportMode          string `json:"import_mode"`
	RemoveAfterImport   bool   `json:"remove_after_import"`
	PollSeconds         int    `json:"poll_seconds"`
	MinSeeders          int    `json:"min_seeders"`
	PreferredResolution int    `json:"preferred_resolution"`

	ReleaseMatchingEnabled    bool `json:"release_matching_enabled"`
	ReleaseMatchingAutoScrape bool `json:"release_matching_auto_scrape"`

	Indexers          []config.IndexerConfig        `json:"indexers"`
	DownloadClients   []config.DownloadClientConfig `json:"download_clients"`
	SupportedIndexers []string                      `json:"supported_indexers"`
	SupportedClients  []string                      `json:"supported_clients"`
}

// GrabStatus assembles the whole torrent/NZB configuration, so the options UI
// can load and save one shape rather than stitching together several endpoints.
func GrabStatus() ResponseGrabStatus {
	indexers := config.Config.Indexers
	if indexers == nil {
		indexers = []config.IndexerConfig{}
	}
	clients := config.Config.DownloadClients
	if clients == nil {
		clients = []config.DownloadClientConfig{}
	}

	return ResponseGrabStatus{
		Enabled:             config.Config.TorrentNZB.Enabled,
		ImportDir:           config.Config.TorrentNZB.ImportDir,
		ImportMode:          config.Config.TorrentNZB.ImportMode,
		RemoveAfterImport:   config.Config.TorrentNZB.RemoveAfterImport,
		PollSeconds:         config.Config.TorrentNZB.PollSeconds,
		MinSeeders:          config.Config.TorrentNZB.MinSeeders,
		PreferredResolution: config.Config.TorrentNZB.PreferredResolution,

		ReleaseMatchingEnabled:    config.Config.ReleaseMatching.Enabled,
		ReleaseMatchingAutoScrape: config.Config.ReleaseMatching.AutoScrape,

		Indexers:          indexers,
		DownloadClients:   clients,
		SupportedIndexers: tasks.SupportedIndexerTypes(),
		SupportedClients:  tasks.SupportedDownloadClients(),
	}
}

func (i GrabResource) status(req *restful.Request, resp *restful.Response) {
	resp.WriteHeaderAndEntity(http.StatusOK, GrabStatus())
}
