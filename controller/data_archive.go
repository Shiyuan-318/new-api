package controller

import (
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

type DataArchiveStatusResponse struct {
	Enabled     bool                              `json:"enabled"`
	StorageMode string                            `json:"storage_mode"`
	MaxFileMB   int                               `json:"max_file_mb"`
	Files       []service.ArchiveFileInfo         `json:"files"`
	TotalBytes  int64                             `json:"total_bytes"`
	Database    *service.DataArchiveDatabaseStats `json:"database,omitempty"`
}

// GetDataArchiveStatus lists local archive volumes (local mode) or the remote
// archive table stats (database mode) for the admin settings page.
func GetDataArchiveStatus(c *gin.Context) {
	setting := operation_setting.GetDataArchiveSetting()
	resp := DataArchiveStatusResponse{
		Enabled:     setting.Enabled,
		StorageMode: setting.NormalizedStorageMode(),
		MaxFileMB:   setting.NormalizedMaxFileMB(),
		Files:       []service.ArchiveFileInfo{},
	}
	files, err := service.ListDataArchiveVolumes()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	resp.Files = files
	for _, f := range files {
		resp.TotalBytes += f.SizeBytes
	}
	if setting.IsDatabaseStorage() {
		stats, err := service.GetDataArchiveDatabaseStats()
		if err != nil {
			common.ApiError(c, err)
			return
		}
		resp.Database = stats
	}
	common.ApiSuccess(c, resp)
}

// DownloadDataArchiveFile streams archived entries as a JSON document.
// Local mode downloads one volume (?file=archive-000001.jsonl); database mode
// exports up to ?limit= rows with created_at older than ?before_ts=.
func DownloadDataArchiveFile(c *gin.Context) {
	setting := operation_setting.GetDataArchiveSetting()
	if !setting.Enabled {
		common.ApiErrorMsg(c, "data archive is disabled")
		return
	}

	var payload []byte
	var fileName string
	var err error

	if setting.IsDatabaseStorage() {
		beforeTs, _ := strconv.ParseInt(c.Query("before_ts"), 10, 64)
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100000"))
		payload, err = service.ExportDataArchiveDatabaseEntries(beforeTs, limit)
		fileName = "request-archive-export.json"
	} else {
		name := c.Query("file")
		if name == "" {
			common.ApiErrorMsg(c, "file parameter is required")
			return
		}
		if !service.IsValidDataArchiveFileName(name) {
			common.ApiErrorMsg(c, "invalid archive file name")
			return
		}
		payload, err = service.LoadDataArchiveVolumeJSON(name)
		fileName = name[:len(name)-len(".jsonl")] + ".json"
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
	c.Data(200, "application/json; charset=utf-8", payload)
}

// DeleteDataArchiveFile removes one local volume (?file=) or, without the
// file parameter in database mode, archived rows older than ?before_ts=.
func DeleteDataArchiveFile(c *gin.Context) {
	setting := operation_setting.GetDataArchiveSetting()
	if setting.IsDatabaseStorage() {
		beforeTs, _ := strconv.ParseInt(c.Query("before_ts"), 10, 64)
		deleted, err := service.DeleteDataArchiveDatabaseEntries(beforeTs)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		common.ApiSuccess(c, gin.H{"deleted": deleted})
		return
	}

	name := c.Query("file")
	if name == "" {
		common.ApiErrorMsg(c, "file parameter is required")
		return
	}
	if !service.IsValidDataArchiveFileName(name) {
		common.ApiErrorMsg(c, "invalid archive file name")
		return
	}
	if err := service.DeleteDataArchiveVolume(name); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// ClearDataArchive removes every entry: all local volumes in local mode, or
// all rows in the archive table in database mode.
func ClearDataArchive(c *gin.Context) {
	setting := operation_setting.GetDataArchiveSetting()
	if setting.IsDatabaseStorage() {
		deleted, err := service.DeleteDataArchiveDatabaseEntries(0)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		common.ApiSuccess(c, gin.H{"deleted": deleted})
		return
	}
	if err := service.DeleteAllDataArchiveVolumes(); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
