package api

import (
	"net/http"
	"strconv"
	"time"

	restfulspec "github.com/emicklei/go-restful-openapi/v2"
	"github.com/emicklei/go-restful/v3"

	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/models"
	"github.com/xbapps/xbvr/pkg/slr"
	"github.com/xbapps/xbvr/pkg/tasks"
	"github.com/xbapps/xbvr/pkg/vrsmash"
)

type RequestDownloadStart struct {
	SceneID uint   `json:"scene_id"`
	Quality string `json:"quality"`
	Codec   string `json:"codec"`
}

type RequestDownloadID struct {
	ID string `json:"id"`
}

type ResponseDownloadFormats struct {
	SceneID    string                    `json:"scene_id"`
	SceneDBID  uint                      `json:"scene_db_id"`
	Title      string                    `json:"title"`
	Provider   string                    `json:"provider"`
	Renditions []tasks.DownloadRendition `json:"renditions"`
}

type ResponseDownloadStatus struct {
	Providers           []tasks.ProviderInfo `json:"providers"`
	SLRSource           string               `json:"slr_cookie_source"`
	VRSMashSource       string               `json:"vrsmash_cookie_source"`
	SLRDownloadDir      string               `json:"slr_download_dir"`
	VRSMashDownloadDir  string               `json:"vrsmash_download_dir"`
	ConcurrentDownloads int                  `json:"concurrent_downloads"`

	VRSMashTokenValid      bool      `json:"vrsmash_token_valid"`
	VRSMashTokenExpires    time.Time `json:"vrsmash_token_expires"`
	VRSMashAutoRefresh     bool      `json:"vrsmash_auto_refresh"`
	VRSMashHasRefreshToken bool      `json:"vrsmash_has_refresh_token"`
}

// DownloadResource exposes the scene downloaders (SexLikeReal, VRsMash).
type DownloadResource struct{}

func (i DownloadResource) WebService() *restful.WebService {
	tags := []string{"Downloads"}

	ws := new(restful.WebService)
	ws.Path("/api/download").
		Consumes(restful.MIME_JSON).
		Produces(restful.MIME_JSON)

	ws.Route(ws.GET("/status").To(i.status).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.GET("/formats/{scene-id}").To(i.formats).
		Param(ws.PathParameter("scene-id", "Internal scene ID").DataType("int")).
		Metadata(restfulspec.KeyOpenAPITags, tags).
		Writes(ResponseDownloadFormats{}))

	ws.Route(ws.POST("/start").To(i.start).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.GET("/list").To(i.list).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.GET("/quota").To(i.quota).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/cancel").To(i.cancel).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/remove").To(i.remove).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	return ws
}

func (i DownloadResource) status(req *restful.Request, resp *restful.Response) {
	tokenExpiry, tokenValid, autoRefresh, hasRefreshToken := vrsmash.TokenStatus()

	resp.WriteHeaderAndEntity(http.StatusOK, ResponseDownloadStatus{
		Providers:           tasks.GetDownloadProviders(),
		SLRSource:           slr.CookieSource(),
		VRSMashSource:       vrsmash.CookieSource(),
		SLRDownloadDir:      config.Config.SLRDownloader.DownloadDir,
		VRSMashDownloadDir:  config.Config.VRSMashDownloader.DownloadDir,
		ConcurrentDownloads: config.Config.SLRDownloader.ConcurrentDownloads,

		VRSMashTokenValid:      tokenValid,
		VRSMashTokenExpires:    tokenExpiry,
		VRSMashAutoRefresh:     autoRefresh,
		VRSMashHasRefreshToken: hasRefreshToken,
	})
}

func (i DownloadResource) formats(req *restful.Request, resp *restful.Response) {
	id, err := strconv.Atoi(req.PathParameter("scene-id"))
	if err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}

	var scene models.Scene
	if err := scene.GetIfExistByPK(uint(id)); err != nil {
		APIError(req, resp, http.StatusNotFound, err)
		return
	}

	renditions, err := tasks.GetDownloadRenditions(scene)
	if err != nil {
		// A JSON body so the dialog can show the reason. A bare status left the
		// user staring at "502 Bad Gateway" with no hint that the session had
		// expired, which is the one case that needs them to do something.
		resp.WriteHeaderAndEntity(http.StatusBadGateway, map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	out := ResponseDownloadFormats{SceneID: scene.SceneID, SceneDBID: scene.ID, Title: scene.Title}
	if len(renditions) > 0 {
		out.Provider = renditions[0].Provider
	}
	out.Renditions = renditions
	resp.WriteHeaderAndEntity(http.StatusOK, out)
}

func (i DownloadResource) start(req *restful.Request, resp *restful.Response) {
	var r RequestDownloadStart
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}

	var scene models.Scene
	if err := scene.GetIfExistByPK(r.SceneID); err != nil {
		APIError(req, resp, http.StatusNotFound, err)
		return
	}

	download, err := tasks.StartDownload(scene, r.Quality, r.Codec)
	if err != nil {
		resp.WriteHeaderAndEntity(http.StatusBadRequest, map[string]interface{}{
			"error":    err.Error(),
			"download": download,
		})
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, download)
}

func (i DownloadResource) list(req *restful.Request, resp *restful.Response) {
	resp.WriteHeaderAndEntity(http.StatusOK, tasks.GetDownloads())
}

func (i DownloadResource) quota(req *restful.Request, resp *restful.Response) {
	provider := req.QueryParameter("provider")
	force := req.QueryParameter("refresh") == "true"

	info, err := tasks.GetDownloadQuota(provider, force)
	if err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, info)
}

func (i DownloadResource) cancel(req *restful.Request, resp *restful.Response) {
	var r RequestDownloadID
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	if err := tasks.CancelDownload(r.ID); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, map[string]string{"status": "cancelled"})
}

func (i DownloadResource) remove(req *restful.Request, resp *restful.Response) {
	var r RequestDownloadID
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	tasks.RemoveDownload(r.ID)
	resp.WriteHeaderAndEntity(http.StatusOK, map[string]string{"status": "removed"})
}
