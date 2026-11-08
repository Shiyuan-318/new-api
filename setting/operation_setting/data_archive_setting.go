package operation_setting

import (
	"github.com/QuantumNous/new-api/setting/config"
)

// DataArchiveSetting controls the request input/output archive feature.
//
// When Enabled is true, every relay request's input (request body) and output
// (response body, truncated) is appended to a local JSONL file first. A daily
// background job runs at SyncHour (default 3 AM, when traffic is lowest) and
// uploads all pending local entries to the archive database described by
// DBType/DSN, deleting local files after a successful upload.
type DataArchiveSetting struct {
	Enabled bool `json:"enabled"`
	// DBType is the archive database dialect: mysql / postgres / sqlite.
	DBType string `json:"db_type"`
	// DSN is the archive database data source name.
	DSN string `json:"dsn"`
	// TableName is the archive table in the archive database.
	TableName string `json:"table_name"`
	// SyncHour is the local hour (0-23) the daily upload runs at.
	SyncHour int `json:"sync_hour"`
	// MaxBodyBytes caps the stored request/response body per entry.
	MaxBodyBytes int `json:"max_body_bytes"`
	// MaxLocalMB caps the local staging directory; writes stop when exceeded.
	MaxLocalMB int `json:"max_local_mb"`
}

var dataArchiveSetting = DataArchiveSetting{
	Enabled:      false,
	DBType:       "mysql",
	DSN:          "",
	TableName:    "request_archive",
	SyncHour:     3,
	MaxBodyBytes: 65536,
	MaxLocalMB:   512,
}

func init() {
	config.GlobalConfig.Register("data_archive_setting", &dataArchiveSetting)
}

func GetDataArchiveSetting() *DataArchiveSetting {
	return &dataArchiveSetting
}

const DataArchiveDefaultTableName = "request_archive"

const DataArchiveMaxBodyBytesLimit = 512 * 1024

func (s *DataArchiveSetting) NormalizedTableName() string {
	if s.TableName == "" {
		return DataArchiveDefaultTableName
	}
	return s.TableName
}

func (s *DataArchiveSetting) NormalizedSyncHour() int {
	if s.SyncHour < 0 || s.SyncHour > 23 {
		return 3
	}
	return s.SyncHour
}

func (s *DataArchiveSetting) NormalizedMaxBodyBytes() int {
	if s.MaxBodyBytes <= 0 {
		return 65536
	}
	return min(s.MaxBodyBytes, DataArchiveMaxBodyBytesLimit)
}

func (s *DataArchiveSetting) NormalizedMaxLocalMB() int {
	if s.MaxLocalMB <= 0 {
		return 512
	}
	return s.MaxLocalMB
}
