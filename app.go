package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/thennavanr/sql-studio/internal/engine"
	"github.com/thennavanr/sql-studio/internal/store"
)

// ErrPasswordRequired tells the UI to prompt for a secret that is not saved.
const ErrPasswordRequired = "PASSWORD_REQUIRED"

// App is bound to the frontend; its exported methods are callable from JS.
type App struct {
	ctx   context.Context
	store *store.Store
	mgr   *engine.Manager

	mu   sync.Mutex
	runs map[string]*run // tab ID -> current run (incl. connecting)
}

type run struct {
	id     string
	cancel context.CancelFunc
}

// NewApp creates the application backend.
func NewApp(st *store.Store) *App {
	return &App{store: st, mgr: engine.NewManager(), runs: map[string]*run{}}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.mgr.OnDeviceCode = func(connID, message, code, url string) {
		runtime.ClipboardSetText(ctx, code)
		runtime.EventsEmit(ctx, "auth:devicecode", map[string]string{
			"connectionId": connID, "message": message, "userCode": code, "url": url,
		})
		runtime.BrowserOpenURL(ctx, url)
	}
}

func (a *App) shutdown(context.Context) { a.mgr.Close() }

// ---- Connections ----

// ListConnections returns saved connection profiles (without secrets).
func (a *App) ListConnections() []store.Connection { return a.store.List() }

// SaveConnection creates or updates a profile. An empty password keeps the saved one.
func (a *App) SaveConnection(c store.Connection) (store.Connection, error) {
	saved, err := a.store.Save(c)
	if err != nil {
		return saved, err
	}
	// Drop live connections so changes take effect.
	if a.mgr.IsRegistered(saved.ID) {
		a.mgr.Disconnect(saved.ID)
	}
	return saved, nil
}

// DeleteConnection removes a profile and its keychain secret.
func (a *App) DeleteConnection(id string) error {
	a.mgr.Disconnect(id)
	return a.store.Delete(id)
}

// HasSavedPassword reports whether a secret exists in the keychain for the profile.
func (a *App) HasSavedPassword(id string) bool {
	c, err := a.store.Get(id)
	return err == nil && c.Password != ""
}

// TestConnection tries a profile (as typed in the dialog) without saving it.
func (a *App) TestConnection(c store.Connection) (engine.ServerInfo, error) {
	if c.Password == "" && c.ID != "" { // editing: fall back to saved secret
		if saved, err := a.store.Get(c.ID); err == nil {
			c.Password = saved.Password
		}
	}
	if c.ID == "" {
		c.ID = "test"
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	return engine.Test(ctx, c, a.mgr.OnDeviceCode)
}

// ConnectResult is returned from Connect.
type ConnectResult struct {
	Info      engine.ServerInfo `json:"info"`
	Databases []string          `json:"databases"`
}

func needsSecret(auth store.AuthType) bool {
	return auth == store.AuthSQL || auth == "" || auth == store.AuthEntraSP
}

// Connect authenticates a saved profile. If its secret is not saved and
// password is empty, the error PASSWORD_REQUIRED is returned.
func (a *App) Connect(id, password string) (ConnectResult, error) {
	c, err := a.store.Get(id)
	if err != nil {
		return ConnectResult{}, err
	}
	if password != "" {
		c.Password = password
	}
	if needsSecret(c.Auth) && c.Password == "" {
		return ConnectResult{}, errors.New(ErrPasswordRequired)
	}
	a.mgr.Register(c)
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	db, err := a.mgr.Pool(ctx, id, c.Database)
	if err != nil {
		a.mgr.Disconnect(id)
		return ConnectResult{}, err
	}
	info, err := engine.Info(ctx, db)
	if err != nil {
		a.mgr.Disconnect(id)
		return ConnectResult{}, err
	}
	dbs, err := engine.Databases(ctx, db)
	if err != nil || len(dbs) == 0 {
		dbs = []string{info.Database}
	}
	return ConnectResult{Info: info, Databases: dbs}, nil
}

// Disconnect closes all pools and editor sessions of a connection.
func (a *App) Disconnect(id string) { a.mgr.Disconnect(id) }

// ---- Object explorer ----

func (a *App) metaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.ctx, 2*time.Minute)
}

// ListDatabases lists databases visible to the connection.
func (a *App) ListDatabases(connID string) ([]string, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	db, err := a.mgr.Pool(ctx, connID, "")
	if err != nil {
		return nil, err
	}
	return engine.Databases(ctx, db)
}

// ListObjects lists tables, views, procedures and functions of a database.
func (a *App) ListObjects(connID, database string) ([]engine.DbObject, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	db, err := a.mgr.Pool(ctx, connID, database)
	if err != nil {
		return nil, err
	}
	return engine.Objects(ctx, db)
}

// ListColumns lists columns of a table or view.
func (a *App) ListColumns(connID, database, schema, name string) ([]engine.ColumnInfo, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	db, err := a.mgr.Pool(ctx, connID, database)
	if err != nil {
		return nil, err
	}
	return engine.Columns(ctx, db, schema, name)
}

// CompletionSchema returns "schema.table" -> columns for autocomplete.
func (a *App) CompletionSchema(connID, database string) (map[string][]string, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	db, err := a.mgr.Pool(ctx, connID, database)
	if err != nil {
		return nil, err
	}
	return engine.CompletionSchema(ctx, db)
}

// ScriptObject returns a CREATE script for an object.
func (a *App) ScriptObject(connID, database, schema, name, kind string) (string, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	db, err := a.mgr.Pool(ctx, connID, database)
	if err != nil {
		return "", err
	}
	return engine.Definition(ctx, db, schema, name, kind)
}

// ScriptData generates INSERT statements with the rows of a table or view.
func (a *App) ScriptData(connID, database, schema, name string, opts engine.ScriptDataOptions) (engine.ScriptDataResult, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	db, err := a.mgr.Pool(ctx, connID, database)
	if err != nil {
		return engine.ScriptDataResult{}, err
	}
	return engine.ScriptData(ctx, db, schema, name, opts)
}

// CopyText puts text on the system clipboard (works for large text and
// outside a user gesture, unlike the web clipboard API).
func (a *App) CopyText(text string) error {
	return runtime.ClipboardSetText(a.ctx, text)
}

// ---- Query execution ----

// eventSink streams execution output to the UI. Every event carries a
// per-run sequence number because the event transport does not guarantee
// delivery order; the UI applies events strictly in sequence.
type eventSink struct {
	ctx   context.Context
	tabID string
	runID string
	mu    sync.Mutex
	seq   int
}

func (s *eventSink) emit(name string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data["tabId"] = s.tabID
	data["runId"] = s.runID
	data["seq"] = s.seq
	s.seq++
	runtime.EventsEmit(s.ctx, name, data)
}
func (s *eventSink) ResultSet(i int, cols []engine.Column) {
	s.emit("exec:resultset", map[string]any{"index": i, "columns": cols})
}
func (s *eventSink) Rows(i int, rows [][]any) {
	s.emit("exec:rows", map[string]any{"index": i, "rows": rows})
}
func (s *eventSink) ResultDone(i, n int, truncated bool) {
	s.emit("exec:resultdone", map[string]any{"index": i, "rowCount": n, "truncated": truncated})
}
func (s *eventSink) Message(m engine.Message) {
	s.emit("exec:message", map[string]any{"message": m})
}

// Execute runs a script in the tab's session asynchronously. Output arrives
// as exec:* events tagged with tabId and the caller-chosen runId.
func (a *App) Execute(tabID, runID, connID, database, script string) {
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	if prev := a.runs[tabID]; prev != nil {
		prev.cancel()
	}
	a.runs[tabID] = &run{id: runID, cancel: cancel}
	a.mu.Unlock()

	sink := &eventSink{ctx: a.ctx, tabID: tabID, runID: runID}
	settings := a.store.Settings()

	go func() {
		defer func() {
			a.mu.Lock()
			if r := a.runs[tabID]; r != nil && r.id == runID {
				delete(a.runs, tabID)
			}
			a.mu.Unlock()
			cancel()
		}()
		start := time.Now()
		var sum engine.Summary
		sess, err := a.mgr.Session(ctx, tabID, connID, database)
		if err != nil {
			sink.Message(engine.Message{Kind: "error", Text: err.Error(), Time: time.Now().UnixMilli()})
			sum = engine.Summary{Errors: 1, Cancelled: ctx.Err() != nil, ElapsedMs: time.Since(start).Milliseconds(), Database: database}
		} else {
			sum = sess.Execute(ctx, script, engine.ExecOptions{
				MaxRows: settings.MaxRows,
				Timeout: time.Duration(settings.QueryTimeout) * time.Second,
			}, sink)
		}
		sink.emit("exec:done", map[string]any{"summary": sum})
		if settings.HistoryEnabled {
			h := store.HistoryEntry{Time: start, ConnectionID: connID, Database: sum.Database, Query: script, DurationMs: sum.ElapsedMs}
			if sum.Errors > 0 {
				h.Error = "error"
			}
			_ = a.store.AddHistory(h)
		}
	}()
}

// Cancel stops the running query of a tab.
func (a *App) Cancel(tabID string) {
	a.mgr.Cancel(tabID)
	a.mu.Lock()
	if r := a.runs[tabID]; r != nil {
		r.cancel()
	}
	a.mu.Unlock()
}

// CloseTab releases the tab's dedicated connection.
func (a *App) CloseTab(tabID string) {
	a.Cancel(tabID)
	a.mgr.CloseSession(tabID)
}

// ---- Settings & history ----

func (a *App) GetSettings() store.Settings               { return a.store.Settings() }
func (a *App) SaveSettings(s store.Settings) error       { return a.store.SaveSettings(s) }
func (a *App) GetHistory() ([]store.HistoryEntry, error) { return a.store.History() }
func (a *App) ClearHistory() error                       { return a.store.ClearHistory() }

// ---- Files ----

// FileResult is a file opened from disk.
type FileResult struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

var sqlFilter = []runtime.FileFilter{{DisplayName: "SQL files (*.sql)", Pattern: "*.sql"}, {DisplayName: "All files", Pattern: "*"}}

// OpenFile shows an open dialog and returns the file (nil if cancelled).
func (a *App) OpenFile() (*FileResult, error) {
	p, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "Open SQL File", Filters: sqlFilter})
	if err != nil || p == "" {
		return nil, err
	}
	return readFile(p)
}

// ReadFile reads a file by path (used for files dropped on the window / recent files).
func (a *App) ReadFile(p string) (*FileResult, error) { return readFile(p) }

func readFile(p string) (*FileResult, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	content := strings.TrimPrefix(string(b), "\uFEFF")
	return &FileResult{Path: p, Name: filepath.Base(p), Content: content}, nil
}

// SaveFile writes content to path, asking for a location when path is empty
// or saveAs is set. Returns the final path ("" if cancelled).
func (a *App) SaveFile(path, suggestedName, content string, saveAs bool) (string, error) {
	if path == "" || saveAs {
		if suggestedName == "" {
			suggestedName = "query.sql"
		}
		p, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
			Title: "Save SQL File", DefaultFilename: suggestedName, Filters: sqlFilter,
		})
		if err != nil || p == "" {
			return "", err
		}
		if filepath.Ext(p) == "" {
			p += ".sql"
		}
		path = p
	}
	return path, os.WriteFile(path, []byte(content), 0o644)
}

// ExportText saves exported results (CSV/JSON/etc) via a save dialog.
func (a *App) ExportText(defaultName, content string) (string, error) {
	ext := strings.TrimPrefix(filepath.Ext(defaultName), ".")
	filters := []runtime.FileFilter{{DisplayName: strings.ToUpper(ext) + " files", Pattern: "*." + ext}}
	p, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: "Export Results", DefaultFilename: defaultName, Filters: filters})
	if err != nil || p == "" {
		return "", err
	}
	return p, os.WriteFile(p, []byte(content), 0o644)
}

// OpenURL opens a link in the default browser.
func (a *App) OpenURL(url string) { runtime.BrowserOpenURL(a.ctx, url) }
