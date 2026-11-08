package operation_setting

import (
	"github.com/QuantumNous/new-api/setting/config"
)

// Storage modes for the request data archive.
const (
	// DataArchiveStorageLocal keeps archived entries as local JSON volume
	// files only. This is the default and requires no external database.
	DataArchiveStorageLocal = "local"
	// DataArchiveStorageDatabase stages entries locally and uploads them to
	// the configured archive database once a day during off-peak hours.
	DataArchiveStorageDatabase = "database"
)

// DataArchiveSetting controls the request input/output archive feature.
//
// When Enabled is true, every relay request's input (request body) and output
// (response body, truncated) is appended to a local JSON volume file first.
// With StorageMode "database" a daily background job runs at SyncHour
// (default 3 AM, when traffic is lowest) and uploads all pending local
// entries to the archive database described by DBType/DSN, deleting local
// files after a successful upload. With StorageMode "local" (the default)
// entries stay on disk as ~MaxFileMB JSON volumes and can be downloaded or
// deleted from the admin settings page.
type DataArchiveSetting struct {
	Enabled bool `json:"enabled"`
	// StorageMode is "local" (default) or "database".
	StorageMode string `json:"storage_mode"`
	// DBType is the archive database dialect: mysql / postgres / sqlite.
	// Only used when StorageMode is "database".
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
	// MaxFileMB is the target size of each local JSON volume file.
	MaxFileMB int `json:"max_file_mb"`
}

var dataArchiveSetting = DataArchiveSetting{
	Enabled:      false,
	StorageMode:  DataArchiveStorageLocal,
	DBType:       "mysql",
	DSN:          "",
	TableName:    "request_archive",
	SyncHour:     3,
	MaxBodyBytes: 65536,
	MaxLocalMB:   512,
	MaxFileMB:    50,
}

func init() {
	config.GlobalConfig.Register("data_archive_setting", &dataArchiveSetting)
}

func GetDataArchiveSetting() *DataArchiveSetting {
	return &dataArchiveSetting
}

const DataArchiveDefaultTableName = "request_archive"

const DataArchiveMaxBodyBytesLimit = 512 * 1024

const DataArchiveMaxFileMBLimit = 1024

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

func (s *DataArchiveSetting) NormalizedMaxFileMB() int {
	if s.MaxFileMB <= 0 {
		return 50
	}
	return min(s.MaxFileMB, DataArchiveMaxFileMBLimit)
}

// NormalizedStorageMode falls back to the default ("local") for unknown values.
func (s *DataArchiveSetting) NormalizedStorageMode() string {
	if s.StorageMode == DataArchiveStorageDatabase {
		return DataArchiveStorageDatabase
	}
	return DataArchiveStorageLocal
}

// IsDatabaseStorage reports whether entries should be uploaded to the
// configured archive database.
func (s *DataArchiveSetting) IsDatabaseStorage() bool {
	return s.NormalizedStorageMode() == DataArchiveStorageDatabase
}
