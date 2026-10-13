package api

import (
	"net/http"
	"strings"

	"github.com/emicklei/go-restful/v3"

	"github.com/xbapps/xbvr/pkg/config"
)

type RequestSaveOptionsVRSMash struct {
	Enabled           bool   `json:"enabled"`
	DownloadDir       string `json:"downloadDir"`
	PreferredCodec    string `json:"preferredCodec"`
	CookieConfigKey   string `json:"cookieConfigKey"`
	UseFirefoxCookies bool   `json:"useFirefoxCookies"`
	AutoRefreshToken  bool   `json:"autoRefreshToken"`
	RefreshToken      string `json:"refreshToken"`
}

func (i ConfigResource) saveOptionsVRSMashDownloader(req *restful.Request, resp *restful.Response) {
	var r RequestSaveOptionsVRSMash
	if err := req.ReadEntity(&r); err != nil {
		log.Error(err)
		return
	}

	if r.PreferredCodec != "h265" {
		r.PreferredCodec = "h264"
	}
	if strings.TrimSpace(r.CookieConfigKey) == "" {
		r.CookieConfigKey = "vrsmash.com-downloader"
	}

	config.Config.VRSMashDownloader.Enabled = r.Enabled
	config.Config.VRSMashDownloader.DownloadDir = strings.TrimSpace(r.DownloadDir)
	config.Config.VRSMashDownloader.PreferredCodec = r.PreferredCodec
	config.Config.VRSMashDownloader.CookieConfigKey = strings.TrimSpace(r.CookieConfigKey)
	config.Config.VRSMashDownloader.UseFirefoxCookies = r.UseFirefoxCookies
	config.Config.VRSMashDownloader.AutoRefreshToken = r.AutoRefreshToken
	config.Config.VRSMashDownloader.RefreshToken = strings.TrimSpace(r.RefreshToken)
	config.SaveConfig()

	resp.WriteHeaderAndEntity(http.StatusOK, config.Config.VRSMashDownloader)
}
