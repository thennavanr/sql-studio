package main

import (
	"context"
	"errors"
	"time"

	driver "go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/thennavanr/sql-studio/internal/engine"
	"github.com/thennavanr/sql-studio/internal/mongo"
	"github.com/thennavanr/sql-studio/internal/store"
)

var errNotForMongo = errors.New("this action is not available for MongoDB connections")

func (a *App) isMongo(connID string) bool {
	c, ok := a.store.Lookup(connID)
	return ok && c.IsMongo()
}

func (a *App) testMongo(ctx context.Context, c store.Connection) (engine.ServerInfo, error) {
	pw := c.Password
	n, err := mongo.Normalize(c)
	if err != nil {
		return engine.ServerInfo{}, err
	}
	if pw != "" {
		n.Password = pw
	}
	return mongo.Test(ctx, n)
}

func (a *App) connectMongo(c store.Connection) (ConnectResult, error) {
	if mongo.NeedsPassword(c) && c.Password == "" {
		return ConnectResult{}, errors.New(ErrPasswordRequired)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	cl, err := a.mongo.Connect(ctx, c)
	if err != nil {
		return ConnectResult{}, err
	}
	info := mongo.Info(ctx, cl, c)
	dbs, err := mongo.Databases(ctx, cl, c)
	if err != nil {
		dbs = nil
	}
	if info.Database == "" && len(dbs) > 0 {
		info.Database = dbs[0]
	}
	return ConnectResult{Info: info, Databases: dbs}, nil
}

func (a *App) mongoClient(connID string) (*mongoClient, error) {
	cl, prof, err := a.mongo.Client(connID)
	if err != nil {
		return nil, err
	}
	return &mongoClient{cl: cl, prof: prof}, nil
}

func (a *App) mongoDatabases(connID string) ([]string, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	m, err := a.mongoClient(connID)
	if err != nil {
		return nil, err
	}
	return mongo.Databases(ctx, m.cl, m.prof)
}

func (a *App) mongoCollections(connID, db string) ([]engine.DbObject, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	m, err := a.mongoClient(connID)
	if err != nil {
		return nil, err
	}
	return mongo.Collections(ctx, m.cl, db)
}

// mongoCompletion returns collection -> sampled field names for autocomplete.
func (a *App) mongoCompletion(connID, db string) (map[string][]string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	m, err := a.mongoClient(connID)
	if err != nil {
		return nil, err
	}
	colls, err := mongo.Collections(ctx, m.cl, db)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for i, c := range colls {
		var fields []string
		if i < 100 { // keep it quick on databases with many collections
			fields, _ = mongo.SampleFields(ctx, m.cl, db, c.Name)
		}
		if fields == nil {
			fields = []string{}
		}
		out[c.Name] = fields
	}
	return out, nil
}

func (a *App) executeMongo(ctx context.Context, connID, db, script string, settings store.Settings, sink *eventSink) engine.Summary {
	m, err := a.mongoClient(connID)
	if err != nil {
		sink.Message(engine.Message{Kind: "error", Text: err.Error(), Time: time.Now().UnixMilli()})
		return engine.Summary{Errors: 1, Database: db}
	}
	if db == "" {
		db = m.prof.Database
		if db == "" {
			db = "test"
		}
	}
	return mongo.Execute(ctx, m.cl, db, script, engine.ExecOptions{
		MaxRows: settings.MaxRows,
		Timeout: time.Duration(settings.QueryTimeout) * time.Second,
	}, sink)
}

// MongoInsertDocument inserts a document (shell syntax) and returns its _id.
func (a *App) MongoInsertDocument(connID, db, coll, doc string) (string, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	m, err := a.mongoClient(connID)
	if err != nil {
		return "", err
	}
	return mongo.InsertDocument(ctx, m.cl, db, coll, doc)
}

// MongoReplaceDocument saves an edited document and returns it as stored.
func (a *App) MongoReplaceDocument(connID, db, coll, id, doc string) (string, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	m, err := a.mongoClient(connID)
	if err != nil {
		return "", err
	}
	return mongo.ReplaceDocument(ctx, m.cl, db, coll, id, doc)
}

// MongoDeleteDocument deletes one document by _id.
func (a *App) MongoDeleteDocument(connID, db, coll, id string) error {
	ctx, cancel := a.metaCtx()
	defer cancel()
	m, err := a.mongoClient(connID)
	if err != nil {
		return err
	}
	return mongo.DeleteDocument(ctx, m.cl, db, coll, id)
}

// Docs streams a MongoDB result's documents to the UI.
func (s *eventSink) Docs(i int, info mongo.DocsInfo) {
	s.emit("exec:docs", map[string]any{"index": i, "info": info})
}

type mongoClient struct {
	cl   *driver.Client
	prof store.Connection
}
