package onepanel

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// Backups and scheduled backups through 1Panel: the files go where 1Panel
// keeps them (its local backup folder, or a backup account such as COS
// set up in 1Panel), and a schedule is one of 1Panel's cron jobs, so it
// runs on the server even when Miao Panel is closed.

// BackupAccount is somewhere 1Panel can keep backups.
type BackupAccount struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // LOCAL, COS, OSS, S3, SFTP, WebDAV, …
}

// BackupAccounts lists where backups can go.
func (c *Client) BackupAccounts(ctx context.Context) ([]BackupAccount, error) {
	var out []BackupAccount
	err := c.do(ctx, http.MethodGet, "/backups/options", nil, &out)
	return out, err
}

// BackupInfo is one backup in 1Panel's list.
type BackupInfo struct {
	ID                uint   `json:"id"`
	CreatedAt         string `json:"createdAt"`
	AccountType       string `json:"accountType"`
	AccountName       string `json:"accountName"`
	DownloadAccountID uint   `json:"downloadAccountID"`
	FileDir           string `json:"fileDir"`
	FileName          string `json:"fileName"`
	TaskID            string `json:"taskID"`
	Status            string `json:"status"` // Waiting, Success, Failed
	Message           string `json:"message"`
	Description       string `json:"description"`
}

// BackupList lists the backups of one thing, newest first as 1Panel
// returns them: kind "website" with the site's alias as name and detail,
// or a database type with the database app and the database's name.
func (c *Client) BackupList(ctx context.Context, kind, name, detail string) ([]BackupInfo, error) {
	var page struct {
		Items []BackupInfo `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/backups/record/search",
		map[string]any{"page": 1, "pageSize": 100, "type": kind, "name": name, "detailName": detail}, &page)
	if page.Items == nil {
		page.Items = []BackupInfo{}
	}
	return page.Items, err
}

// FetchBackup makes a backup's file available on the server (fetching it
// from a remote account when needed) and returns its path there.
func (c *Client) FetchBackup(ctx context.Context, b BackupInfo) (string, error) {
	var path string
	err := c.do(ctx, http.MethodPost, "/backups/record/download",
		map[string]any{"downloadAccountID": b.DownloadAccountID, "fileDir": b.FileDir, "fileName": b.FileName}, &path)
	return path, err
}

// Restore puts a backup back. 1Panel backs the current state up to its
// temporary folder first and returns to it when restoring fails.
func (c *Client) Restore(ctx context.Context, kind, name, detail string, b BackupInfo, file, taskID string) error {
	return c.do(ctx, http.MethodPost, "/backups/recover", map[string]any{
		"downloadAccountID": b.DownloadAccountID, "type": kind, "name": name, "detailName": detail,
		"file": file, "backupRecordID": b.ID, "taskID": taskID,
	}, nil)
}

// Task is one of 1Panel's background tasks, such as a restore.
type Task struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // Executing, Success, Failed
	ErrorMsg string `json:"errorMsg"`
}

// Task looks up a background task; found is false while 1Panel has not
// recorded it yet.
func (c *Client) Task(ctx context.Context, id string) (t Task, found bool, err error) {
	var page struct {
		Items []Task `json:"items"`
	}
	err = c.do(ctx, http.MethodPost, "/logs/tasks/search", map[string]any{"page": 1, "pageSize": 10, "taskID": id}, &page)
	for _, x := range page.Items {
		if x.ID == id {
			return x, true, err
		}
	}
	return Task{}, false, err
}

// DeleteBackups removes backups and their files.
func (c *Client) DeleteBackups(ctx context.Context, ids []uint) error {
	return c.do(ctx, http.MethodPost, "/backups/record/del", map[string]any{"ids": ids}, nil)
}

// Cronjob is one of 1Panel's scheduled tasks.
type Cronjob struct {
	ID                uint   `json:"id,omitempty"`
	Name              string `json:"name"`
	Type              string `json:"type"` // website, database, directory, shell, …
	Spec              string `json:"spec"` // standard cron: minute hour day month weekday
	SpecCustom        bool   `json:"specCustom"`
	Website           string `json:"website,omitempty"` // site ids, comma-separated, or "all"
	DBType            string `json:"dbType,omitempty"`
	DBName            string `json:"dbName,omitempty"`
	RetainCopies      int    `json:"retainCopies"`
	RetryTimes        int    `json:"retryTimes"`
	Timeout           uint   `json:"timeout"`
	SourceAccountIDs  string `json:"sourceAccountIDs"` // backup account ids, comma-separated
	DownloadAccountID uint   `json:"downloadAccountID"`
	Status            string `json:"status,omitempty"` // Enable, Disable
	LastRecordStatus  string `json:"lastRecordStatus,omitempty"`
	LastRecordTime    string `json:"lastRecordTime,omitempty"`
}

// AccountIDs returns where a job keeps its backups.
func (j Cronjob) AccountIDs() []uint {
	var out []uint
	for _, s := range strings.Split(j.SourceAccountIDs, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			out = append(out, uint(n))
		}
	}
	return out
}

// Cronjobs lists the scheduled tasks whose name contains info.
func (c *Client) Cronjobs(ctx context.Context, info string) ([]Cronjob, error) {
	var page struct {
		Items []Cronjob `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/cronjobs/search",
		map[string]any{"page": 1, "pageSize": 200, "info": info, "groupIDs": []uint{}, "orderBy": "createdAt", "order": "null"}, &page)
	if page.Items == nil {
		page.Items = []Cronjob{}
	}
	return page.Items, err
}

// SaveCronjob creates a scheduled task, or changes it when it has an id.
func (c *Client) SaveCronjob(ctx context.Context, j Cronjob) error {
	if j.Timeout == 0 {
		j.Timeout = 3600
	}
	path := "/cronjobs"
	if j.ID != 0 {
		path = "/cronjobs/update"
	}
	return c.do(ctx, http.MethodPost, path, j, nil)
}

// SetCronjobStatus switches a scheduled task on (Enable) or off (Disable).
func (c *Client) SetCronjobStatus(ctx context.Context, id uint, status string) error {
	return c.do(ctx, http.MethodPost, "/cronjobs/status", map[string]any{"id": id, "status": status}, nil)
}

// DeleteCronjob removes a scheduled task, keeping the backups it made.
func (c *Client) DeleteCronjob(ctx context.Context, id uint) error {
	return c.do(ctx, http.MethodPost, "/cronjobs/del", map[string]any{"ids": []uint{id}, "cleanData": false, "cleanRemoteData": false}, nil)
}
