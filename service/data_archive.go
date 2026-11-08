package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The archive feature stages every relay request's input/output on the local
// disk as JSON volumes (one ~MaxFileMB JSONL volume at a time). With the
// "database" storage mode a daily off-peak job uploads staged entries to the
// configured archive database and deletes local files afterwards; with the
// default "local" mode entries stay on disk and can be downloaded or deleted
// from the admin settings page. See operation_setting.DataArchiveSetting.

const (
	dataArchiveDir           = "data/archive"
	dataArchiveFilePrefix    = "archive-"
	dataArchiveFileSuffix    = ".jsonl"
	dataArchiveStateFileName = ".last_sync"
	dataArchiveBatchSize     = 200
	dataArchiveVolumeDigits  = 6
)

type RelayArchiveEntry struct {
	RequestId        string `json:"request_id"`
	UserId           int    `json:"user_id"`
	Username         string `json:"username"`
	TokenName        string `json:"token_name"`
	TokenId          int    `json:"token_id"`
	ModelName        string `json:"model_name"`
	Group            string `json:"group"`
	ChannelId        int    `json:"channel_id"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Quota            int    `json:"quota"`
	UseTimeSeconds   int    `json:"use_time_seconds"`
	IsStream         bool   `json:"is_stream"`
	Success          bool   `json:"success"`
	RequestBody      string `json:"request_body"`
	ResponseBody     string `json:"response_body"`
	NodeName         string `json:"node_name"`
	CreatedAt        int64  `json:"created_at"`
}

// RequestArchive is the row persisted into the remote archive database.
// String columns intentionally carry no explicit size: gorm maps them to
// LONGTEXT (mysql) / text (postgres, sqlite) which fits truncated bodies.
type RequestArchive struct {
	Id               int64  `json:"id" gorm:"primaryKey"`
	RequestId        string `json:"request_id" gorm:"size:64;index"`
	UserId           int    `json:"user_id" gorm:"index"`
	Username         string `json:"username" gorm:"size:64;index"`
	TokenName        string `json:"token_name" gorm:"size:64"`
	TokenId          int    `json:"token_id"`
	ModelName        string `json:"model_name" gorm:"size:128;index"`
	Group            string `json:"group" gorm:"size:64"`
	ChannelId        int    `json:"channel_id"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Quota            int    `json:"quota"`
	UseTimeSeconds   int    `json:"use_time_seconds"`
	IsStream         bool   `json:"is_stream"`
	Success          bool   `json:"success"`
	RequestBody      string `json:"request_body"`
	ResponseBody     string `json:"response_body"`
	NodeName         string `json:"node_name" gorm:"size:64"`
	CreatedAt        int64  `json:"created_at" gorm:"index"`
	ArchivedAt       int64  `json:"archived_at"`
}

var (
	dataArchiveWriteMu   sync.Mutex
	dataArchiveOverLimit atomic.Bool
)

// DataArchiveCaptureEnabled reports whether relay requests should be captured.
func DataArchiveCaptureEnabled() bool {
	return operation_setting.GetDataArchiveSetting().Enabled
}

// ArchiveResponseWriter wraps gin.ResponseWriter and keeps a bounded copy of
// everything written to the client, so the relay's response body can be
// archived together with its request body.
type ArchiveResponseWriter struct {
	gin.ResponseWriter
	body    *bytes.Buffer
	maxSize int
}

// NewArchiveResponseWriter wraps writer. maxSize bounds the buffered body.
func NewArchiveResponseWriter(writer gin.ResponseWriter, maxSize int) *ArchiveResponseWriter {
	if maxSize <= 0 {
		maxSize = operation_setting.GetDataArchiveSetting().NormalizedMaxBodyBytes()
	}
	return &ArchiveResponseWriter{
		ResponseWriter: writer,
		body:           bytes.NewBuffer(nil),
		maxSize:        maxSize,
	}
}

func (w *ArchiveResponseWriter) Write(b []byte) (int, error) {
	if w.body.Len() < w.maxSize {
		remain := w.maxSize - w.body.Len()
		if remain >= len(b) {
			w.body.Write(b)
		} else {
			w.body.Write(b[:remain])
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *ArchiveResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// Body returns the bounded response body captured so far.
func (w *ArchiveResponseWriter) Body() []byte {
	return w.body.Bytes()
}

// SnapshotArchiveRequestBody reads a bounded snapshot of the relay request
// body from the reusable body storage without disturbing later readers. It
// returns nil when the body storage is unavailable or too large to archive.
func SnapshotArchiveRequestBody(c *gin.Context, maxBytes int) []byte {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil
	}
	maxCapture := int64(maxBytes)
	if maxCapture <= 0 {
		maxCapture = int64(operation_setting.GetDataArchiveSetting().NormalizedMaxBodyBytes())
	}
	// Oversized bodies are skipped entirely: streaming the whole payload back
	// just to truncate it would defeat the disk-backed storage design.
	if storage.Size() > maxCapture*2 {
		return nil
	}
	if _, err := storage.Seek(0, 0); err != nil {
		return nil
	}
	data, err := storage.Bytes()
	if _, seekErr := storage.Seek(0, 0); seekErr != nil {
		return nil
	}
	if err != nil {
		return nil
	}
	if int64(len(data)) > maxCapture {
		data = data[:maxCapture]
	}
	return data
}

// CaptureRelayArchive stages one finished relay request locally. Failures to
// write never affect the request itself; they are logged once per overflow.
func CaptureRelayArchive(c *gin.Context, relayInfo *relaycommon.RelayInfo, requestBody []byte, responseBody []byte, success bool) {
	setting := operation_setting.GetDataArchiveSetting()
	if !setting.Enabled || relayInfo == nil {
		return
	}
	entry := RelayArchiveEntry{
		RequestId:        c.GetString(common.RequestIdKey),
		UserId:           relayInfo.UserId,
		TokenName:        c.GetString("token_name"),
		TokenId:          relayInfo.TokenId,
		ModelName:        relayInfo.OriginModelName,
		Group:            relayInfo.UsingGroup,
		ChannelId:        relayInfo.ChannelId,
		CompletionTokens: int(relayInfo.PerformanceOutputTokens),
		IsStream:         relayInfo.IsStream,
		Success:          success,
		NodeName:         common.NodeName,
		CreatedAt:        time.Now().Unix(),
	}
	if username := c.GetString("username"); username != "" {
		entry.Username = username
	}
	maxBody := setting.NormalizedMaxBodyBytes()
	if len(requestBody) > maxBody {
		requestBody = requestBody[:maxBody]
	}
	if len(responseBody) > maxBody {
		responseBody = responseBody[:maxBody]
	}
	entry.RequestBody = string(requestBody)
	entry.ResponseBody = string(responseBody)

	gopool.Go(func() {
		err := appendLocalArchive(entry, setting.NormalizedMaxLocalMB(), setting.NormalizedMaxFileMB())
		if err != nil {
			if dataArchiveOverLimit.CompareAndSwap(false, true) {
				common.SysError(fmt.Sprintf("data archive: stop staging locally: %s", err.Error()))
			}
			return
		}
		dataArchiveOverLimit.Store(false)
	})
}

func dataArchiveDirPath() string {
	return dataArchiveDir
}

// IsValidDataArchiveFileName guards against path traversal: only volume files
// matching the archive-*.jsonl naming scheme are accepted by management APIs.
func IsValidDataArchiveFileName(name string) bool {
	if !strings.HasPrefix(name, dataArchiveFilePrefix) || !strings.HasSuffix(name, dataArchiveFileSuffix) {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, dataArchiveFilePrefix), dataArchiveFileSuffix)
	if len(body) != dataArchiveVolumeDigits {
		return false
	}
	for _, r := range body {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func archiveVolumeNumber(name string) (int, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(name, dataArchiveFilePrefix), dataArchiveFileSuffix)
	return strconv.Atoi(body)
}

// appendLocalArchive appends one entry to the current JSON volume file,
// rolling over to a new volume once the active file reaches MaxFileMB.
func appendLocalArchive(entry RelayArchiveEntry, maxLocalMB int, maxFileMB int) error {
	if err := os.MkdirAll(dataArchiveDirPath(), 0755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}
	if err := ensureLocalSizeWithinLimit(maxLocalMB); err != nil {
		return err
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal archive entry: %w", err)
	}

	dataArchiveWriteMu.Lock()
	defer dataArchiveWriteMu.Unlock()
	fileName, err := activeArchiveVolumePath(int64(maxFileMB) << 20)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open archive file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write archive entry: %w", err)
	}
	return nil
}

// activeArchiveVolumePath returns the file to append to. The caller must hold
// dataArchiveWriteMu. A fresh volume is started when the newest volume is at
// or beyond maxFileBytes.
func activeArchiveVolumePath(maxFileBytes int64) (string, error) {
	entries, err := os.ReadDir(dataArchiveDirPath())
	if err != nil {
		return "", err
	}
	highest := 0
	var highestSize int64
	for _, e := range entries {
		if e.IsDir() || !IsValidDataArchiveFileName(e.Name()) {
			continue
		}
		num, err := archiveVolumeNumber(e.Name())
		if err != nil || num <= 0 {
			continue
		}
		if num > highest {
			if info, err := e.Info(); err == nil {
				highestSize = info.Size()
			}
			highest = num
		}
	}
	if highest > 0 && highestSize < maxFileBytes {
		return filepath.Join(dataArchiveDirPath(), fmt.Sprintf("%s%0*d%s", dataArchiveFilePrefix, dataArchiveVolumeDigits, highest, dataArchiveFileSuffix)), nil
	}
	return filepath.Join(dataArchiveDirPath(), fmt.Sprintf("%s%0*d%s", dataArchiveFilePrefix, dataArchiveVolumeDigits, highest+1, dataArchiveFileSuffix)), nil
}

func ensureLocalSizeWithinLimit(maxLocalMB int) error {
	limitBytes := int64(maxLocalMB) << 20
	total, err := dataArchiveDirSize()
	if err != nil {
		return err
	}
	if total >= limitBytes {
		return fmt.Errorf("local archive exceeds %d MB limit", maxLocalMB)
	}
	return nil
}

func dataArchiveDirSize() (int64, error) {
	entries, err := os.ReadDir(dataArchiveDirPath())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total, nil
}

// StartDataArchiveSyncTask launches the daily off-peak upload loop. It ticks
// every 10 minutes and uploads pending local archives once per day, when the
// local hour matches the configured SyncHour. Only the "database" storage
// mode uploads; "local" keeps the volumes on disk.
func StartDataArchiveSyncTask() {
	go func() {
		for {
			now := time.Now()
			setting := operation_setting.GetDataArchiveSetting()
			if setting.Enabled && setting.IsDatabaseStorage() {
				if now.Hour() == setting.NormalizedSyncHour() {
					if err := SyncLocalArchivesToDatabase(); err != nil {
						common.SysError(fmt.Sprintf("data archive sync failed: %s", err.Error()))
					}
				}
			}
			time.Sleep(10 * time.Minute)
		}
	}()
}

// SyncLocalArchivesToDatabase uploads every pending local JSONL file to the
// archive database, deleting files whose rows were fully persisted. A state
// file guards against running twice in the same day (e.g. process restarts).
func SyncLocalArchivesToDatabase() error {
	today := time.Now().Format("2006-01-02")
	statePath := filepath.Join(dataArchiveDirPath(), dataArchiveStateFileName)
	if data, err := os.ReadFile(statePath); err == nil && strings.TrimSpace(string(data)) == today {
		return nil
	}

	setting := operation_setting.GetDataArchiveSetting()
	files, err := pendingArchiveFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		_ = os.WriteFile(statePath, []byte(today), 0644)
		return nil
	}

	archiveDB, err := openArchiveDB(setting)
	if err != nil {
		return err
	}
	sqlDB, err := archiveDB.DB()
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	tableName := setting.NormalizedTableName()
	if err := archiveDB.Table(tableName).AutoMigrate(&RequestArchive{}); err != nil {
		return fmt.Errorf("migrate archive table: %w", err)
	}

	var firstErr error
	for _, file := range files {
		if err := syncOneArchiveFile(archiveDB, tableName, file); err != nil {
			common.SysError(fmt.Sprintf("data archive: upload %s failed: %s", filepath.Base(file), err.Error()))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.Remove(file); err != nil {
			common.SysError(fmt.Sprintf("data archive: remove %s failed: %s", filepath.Base(file), err.Error()))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	_ = os.WriteFile(statePath, []byte(today), 0644)
	common.SysLog("data archive: uploaded pending archives successfully")
	return nil
}

func pendingArchiveFiles() ([]string, error) {
	entries, err := os.ReadDir(dataArchiveDirPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, dataArchiveFilePrefix) && strings.HasSuffix(name, dataArchiveFileSuffix) {
			files = append(files, filepath.Join(dataArchiveDirPath(), name))
		}
	}
	sort.Strings(files)
	return files, nil
}

func syncOneArchiveFile(db *gorm.DB, tableName string, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	rows := make([]RequestArchive, 0, 64)
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		if err := db.Table(tableName).CreateInBatches(rows, dataArchiveBatchSize).Error; err != nil {
			return err
		}
		rows = rows[:0]
		return nil
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry RelayArchiveEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip corrupted lines instead of failing the whole file.
			continue
		}
		rows = append(rows, RequestArchive{
			RequestId:        entry.RequestId,
			UserId:           entry.UserId,
			Username:         entry.Username,
			TokenName:        entry.TokenName,
			TokenId:          entry.TokenId,
			ModelName:        entry.ModelName,
			Group:            entry.Group,
			ChannelId:        entry.ChannelId,
			PromptTokens:     entry.PromptTokens,
			CompletionTokens: entry.CompletionTokens,
			Quota:            entry.Quota,
			UseTimeSeconds:   entry.UseTimeSeconds,
			IsStream:         entry.IsStream,
			Success:          entry.Success,
			RequestBody:      entry.RequestBody,
			ResponseBody:     entry.ResponseBody,
			NodeName:         entry.NodeName,
			CreatedAt:        entry.CreatedAt,
			ArchivedAt:       now,
		})
		if len(rows) >= dataArchiveBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func openArchiveDB(setting *operation_setting.DataArchiveSetting) (*gorm.DB, error) {
	gormConfig := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	}
	switch strings.ToLower(setting.DBType) {
	case "mysql":
		if setting.DSN == "" {
			return nil, fmt.Errorf("archive database DSN is empty")
		}
		return gorm.Open(mysql.Open(setting.DSN), gormConfig)
	case "postgres", "postgresql":
		if setting.DSN == "" {
			return nil, fmt.Errorf("archive database DSN is empty")
		}
		return gorm.Open(postgres.Open(setting.DSN), gormConfig)
	case "sqlite":
		dsn := setting.DSN
		if dsn == "" {
			return nil, fmt.Errorf("archive database DSN is empty")
		}
		return gorm.Open(sqlite.Open(dsn), gormConfig)
	default:
		return nil, fmt.Errorf("unsupported archive database type: %s", setting.DBType)
	}
}

// ----------------------------------------------------------------------------
// Archive management (admin): list, download, delete
// ----------------------------------------------------------------------------

// ArchiveFileInfo describes one local JSON volume file.
type ArchiveFileInfo struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Entries   int64  `json:"entries"`
	ModTime   int64  `json:"mod_time"`
}

// DataArchiveDatabaseStats describes the remote archive table contents.
type DataArchiveDatabaseStats struct {
	Table   string `json:"table"`
	Entries int64  `json:"entries"`
}

// ListDataArchiveVolumes lists local volume files sorted oldest first, with
// entry counts.
func ListDataArchiveVolumes() ([]ArchiveFileInfo, error) {
	entries, err := os.ReadDir(dataArchiveDirPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []ArchiveFileInfo{}, nil
		}
		return nil, err
	}
	files := make([]ArchiveFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !IsValidDataArchiveFileName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		entryCount := int64(0)
		if lines, err := countArchiveLines(filepath.Join(dataArchiveDirPath(), e.Name())); err == nil {
			entryCount = lines
		}
		files = append(files, ArchiveFileInfo{
			Name:      e.Name(),
			SizeBytes: info.Size(),
			Entries:   entryCount,
			ModTime:   info.ModTime().Unix(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func countArchiveLines(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count, nil
}

// LoadDataArchiveVolumeJSON reads one volume and returns its entries as a
// standard JSON array document (the on-disk volume is JSON Lines).
func LoadDataArchiveVolumeJSON(name string) ([]byte, error) {
	if !IsValidDataArchiveFileName(name) {
		return nil, fmt.Errorf("invalid archive file name")
	}
	data, err := os.ReadFile(filepath.Join(dataArchiveDirPath(), name))
	if err != nil {
		return nil, err
	}
	entries := make([]RelayArchiveEntry, 0, 1024)
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry RelayArchiveEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip corrupted lines instead of failing the whole volume.
			continue
		}
		entries = append(entries, entry)
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteDataArchiveVolume removes one local volume file by name.
func DeleteDataArchiveVolume(name string) error {
	if !IsValidDataArchiveFileName(name) {
		return fmt.Errorf("invalid archive file name")
	}
	dataArchiveWriteMu.Lock()
	defer dataArchiveWriteMu.Unlock()
	return os.Remove(filepath.Join(dataArchiveDirPath(), name))
}

// DeleteAllDataArchiveVolumes removes every local volume file.
func DeleteAllDataArchiveVolumes() error {
	dataArchiveWriteMu.Lock()
	defer dataArchiveWriteMu.Unlock()
	entries, err := os.ReadDir(dataArchiveDirPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !IsValidDataArchiveFileName(e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dataArchiveDirPath(), e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// GetDataArchiveDatabaseStats connects to the configured archive database and
// reports the row count of the archive table.
func GetDataArchiveDatabaseStats() (*DataArchiveDatabaseStats, error) {
	setting := operation_setting.GetDataArchiveSetting()
	db, err := openArchiveDB(setting)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sqlDB.Close() }()
	tableName := setting.NormalizedTableName()
	if err := db.Table(tableName).AutoMigrate(&RequestArchive{}); err != nil {
		return nil, fmt.Errorf("migrate archive table: %w", err)
	}
	var count int64
	if err := db.Table(tableName).Count(&count).Error; err != nil {
		return nil, err
	}
	return &DataArchiveDatabaseStats{Table: tableName, Entries: count}, nil
}

// DeleteDataArchiveDatabaseEntries deletes archived rows older than beforeTs
// (0 deletes everything) and returns the number of deleted rows.
func DeleteDataArchiveDatabaseEntries(beforeTs int64) (int64, error) {
	setting := operation_setting.GetDataArchiveSetting()
	db, err := openArchiveDB(setting)
	if err != nil {
		return 0, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return 0, err
	}
	defer func() { _ = sqlDB.Close() }()
	tableName := setting.NormalizedTableName()
	query := db.Table(tableName)
	if beforeTs > 0 {
		query = query.Where("created_at < ?", beforeTs)
	}
	result := query.Delete(&RequestArchive{})
	return result.RowsAffected, result.Error
}

// ExportDataArchiveDatabaseEntries queries archived rows (created_at older
// than beforeTs when given, capped by limit) and returns them as a JSON array
// document.
func ExportDataArchiveDatabaseEntries(beforeTs int64, limit int) ([]byte, error) {
	setting := operation_setting.GetDataArchiveSetting()
	db, err := openArchiveDB(setting)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sqlDB.Close() }()
	tableName := setting.NormalizedTableName()
	query := db.Table(tableName).Order("created_at ASC")
	if beforeTs > 0 {
		query = query.Where("created_at < ?", beforeTs)
	}
	if limit <= 0 || limit > 100000 {
		limit = 100000
	}
	var rows []RequestArchive
	if err := query.Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}
