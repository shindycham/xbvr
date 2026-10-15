package api

import (
	"errors"
	"net/http"
	"strconv"

	restfulspec "github.com/emicklei/go-restful-openapi/v2"
	"github.com/emicklei/go-restful/v3"

	"github.com/xbapps/xbvr/pkg/tasks"
)

type RequestDuplicateMerge struct {
	PrimarySceneID    uint   `json:"primary_scene_id"`
	DuplicateSceneIDs []uint `json:"duplicate_scene_ids"`
}

type RequestDuplicateUnmerge struct {
	DuplicateSceneID uint `json:"duplicate_scene_id"`
}

// DuplicateResource finds scenes that look like the same scene from different
// sites, and merges the ones a person confirms.
type DuplicateResource struct{}

func (i DuplicateResource) WebService() *restful.WebService {
	tags := []string{"Scenes"}

	ws := new(restful.WebService)
	ws.Path("/api/duplicates").
		Consumes(restful.MIME_JSON).
		Produces(restful.MIME_JSON)

	ws.Route(ws.GET("/candidates").To(i.candidates).
		Param(ws.QueryParameter("limit", "Maximum number of groups to return").DataType("int")).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.GET("/merged").To(i.merged).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/merge").To(i.merge).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	ws.Route(ws.POST("/unmerge").To(i.unmerge).
		Metadata(restfulspec.KeyOpenAPITags, tags))

	return ws
}

func (i DuplicateResource) candidates(req *restful.Request, resp *restful.Response) {
	limit := 100
	if raw := req.QueryParameter("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	groups, err := tasks.FindDuplicateScenes(limit)
	if err != nil {
		APIError(req, resp, http.StatusInternalServerError, err)
		return
	}
	if groups == nil {
		groups = []tasks.DuplicateGroup{}
	}
	resp.WriteHeaderAndEntity(http.StatusOK, groups)
}

func (i DuplicateResource) merged(req *restful.Request, resp *restful.Response) {
	merges, err := tasks.GetMergedScenes()
	if err != nil {
		APIError(req, resp, http.StatusInternalServerError, err)
		return
	}
	if merges == nil {
		merges = []tasks.MergedScene{}
	}
	resp.WriteHeaderAndEntity(http.StatusOK, merges)
}

func (i DuplicateResource) merge(req *restful.Request, resp *restful.Response) {
	var r RequestDuplicateMerge
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	if r.PrimarySceneID == 0 {
		APIError(req, resp, http.StatusBadRequest, errors.New("no scene was chosen to keep"))
		return
	}
	if len(r.DuplicateSceneIDs) == 0 {
		APIError(req, resp, http.StatusBadRequest, errors.New("no scenes were chosen to merge"))
		return
	}

	merged, err := tasks.MergeScenes(r.PrimarySceneID, r.DuplicateSceneIDs)
	if err != nil {
		// A partial merge is worth reporting honestly rather than as a failure:
		// some duplicates may have been combined before the problem was hit.
		resp.WriteHeaderAndEntity(http.StatusBadRequest, map[string]interface{}{
			"error":  err.Error(),
			"merged": merged,
		})
		return
	}

	resp.WriteHeaderAndEntity(http.StatusOK, map[string]interface{}{"merged": merged})
}

func (i DuplicateResource) unmerge(req *restful.Request, resp *restful.Response) {
	var r RequestDuplicateUnmerge
	if err := req.ReadEntity(&r); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	if err := tasks.UnmergeScene(r.DuplicateSceneID); err != nil {
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}
	resp.WriteHeaderAndEntity(http.StatusOK, map[string]string{"status": "unmerged"})
}
