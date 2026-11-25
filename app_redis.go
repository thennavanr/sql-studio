package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/thennavanr/sql-studio/internal/engine"
	"github.com/thennavanr/sql-studio/internal/redisdb"
	"github.com/thennavanr/sql-studio/internal/store"
)

func (a *App) isRedis(connID string) bool {
	c, ok := a.store.Lookup(connID)
	return ok && c.IsRedis()
}

func (a *App) testRedis(ctx context.Context, c store.Connection) (engine.ServerInfo, error) {
	pw := c.Password
	n, err := redisdb.Normalize(c)
	if err != nil {
		return engine.ServerInfo{}, err
	}
	if pw != "" {
		n.Password = pw
	}
	return redisdb.Test(ctx, n)
}

// connectRedis connects without requiring a password: many Redis servers
// have none. A server that needs one replies NOAUTH, which is reported.
func (a *App) connectRedis(c store.Connection) (ConnectResult, error) {
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	cl, err := a.redis.Connect(ctx, c)
	if err != nil {
		if c.Password == "" && isAuthError(err) {
			return ConnectResult{}, errPasswordRequired
		}
		return ConnectResult{}, err
	}
	info := redisdb.Info(ctx, cl, c)
	dbs, err := redisdb.Databases(ctx, cl, c)
	if err != nil {
		dbs = []string{c.Database}
	}
	return ConnectResult{Info: info, Databases: dbs}, nil
}

func (a *App) redisClient(ctx context.Context, connID, database string) (*redis.Client, int, error) {
	c, ok := a.redis.Profile(connID)
	if !ok {
		return nil, 0, errNotConnected
	}
	db, ok := redisdb.ParseDBName(database)
	if !ok {
		db, _ = redisdb.ParseDBName(c.Database)
	}
	cl, err := a.redis.Client(ctx, connID, db)
	return cl, db, err
}

func (a *App) redisDatabases(connID string) ([]string, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	c, ok := a.redis.Profile(connID)
	if !ok {
		return nil, errNotConnected
	}
	cl, _, err := a.redisClient(ctx, connID, c.Database)
	if err != nil {
		return nil, err
	}
	return redisdb.Databases(ctx, cl, c)
}

func (a *App) redisKeys(connID, database string) ([]engine.DbObject, error) {
	ctx, cancel := a.metaCtx()
	defer cancel()
	cl, _, err := a.redisClient(ctx, connID, database)
	if err != nil {
		return nil, err
	}
	return redisdb.Keys(ctx, cl, "")
}

// redisCompletion returns sampled key names (as map keys) for autocomplete.
func (a *App) redisCompletion(connID, database string) (map[string][]string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	cl, _, err := a.redisClient(ctx, connID, database)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, k := range redisdb.SampleKeys(ctx, cl, 500) {
		out[k] = []string{}
	}
	return out, nil
}

func (a *App) executeRedis(ctx context.Context, connID, database, script string, settings store.Settings, sink *eventSink) engine.Summary {
	_, db, err := a.redisClient(ctx, connID, database)
	if err != nil {
		sink.Message(engine.Message{Kind: "error", Text: err.Error(), Time: time.Now().UnixMilli()})
		return engine.Summary{Errors: 1, Database: database}
	}
	clients := func(ctx context.Context, n int) (*redis.Client, error) { return a.redis.Client(ctx, connID, n) }
	return redisdb.Execute(ctx, clients, db, script, engine.ExecOptions{
		MaxRows: settings.MaxRows,
		Timeout: time.Duration(settings.QueryTimeout) * time.Second,
	}, sink)
}

func (a *App) validateRedis(ctx context.Context, connID, database, script string) ([]engine.Message, error) {
	cl, _, err := a.redisClient(ctx, connID, database)
	if err != nil {
		return nil, err
	}
	return redisdb.Validate(ctx, cl, script), nil
}

// RedisDangerousCommands lists commands the UI asks to confirm before running.
func (a *App) RedisDangerousCommands() []string { return redisdb.DangerousCommands }

var (
	errNotConnected     = errors.New("not connected")
	errPasswordRequired = errors.New(ErrPasswordRequired)
)

func isAuthError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "NOAUTH") || strings.Contains(msg, "WRONGPASS") || strings.Contains(msg, "Authentication required")
}
