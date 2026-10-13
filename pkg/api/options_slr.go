package api

import (
	"net/http"
	"strings"

	"github.com/emicklei/go-restful/v3"

	"github.com/xbapps/xbvr/pkg/config"
)

func (i ConfigResource) saveOptionsSLRDownloader(req *restful.Request, resp *restful.Response) {
	var r RequestSaveOptionsSLRDownloader
	if err := req.ReadEntity(&r); err != nil {
		log.Error(err)
		return
	}

	if r.PreferredCodec != "h265" {
		r.PreferredCodec = "h264"
	}
	if r.ConcurrentDownloads < 1 {
		r.ConcurrentDownloads = 1
	}
	if strings.TrimSpace(r.CookieConfigKey) == "" {
		r.CookieConfigKey = "sexlikereal.com-downloader"
	}

	config.Config.SLRDownloader.Enabled = r.Enabled
	config.Config.SLRDownloader.DownloadDir = strings.TrimSpace(r.DownloadDir)
	config.Config.SLRDownloader.PreferredCodec = r.PreferredCodec
	config.Config.SLRDownloader.CookieConfigKey = strings.TrimSpace(r.CookieConfigKey)
	config.Config.SLRDownloader.UseFirefoxCookies = r.UseFirefoxCookies
	config.Config.SLRDownloader.ConcurrentDownloads = r.ConcurrentDownloads
	config.Config.SLRDownloader.RescanAfterDownload = r.RescanAfterDownload
	config.SaveConfig()

	resp.WriteHeaderAndEntity(http.StatusOK, config.Config.SLRDownloader)
}
