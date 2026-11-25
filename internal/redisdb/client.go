// Package redisdb adds Redis support: connection handling, a key browser and
// redis-cli style command execution.
package redisdb

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/thennavanr/sql-studio/internal/engine"
	"github.com/thennavanr/sql-studio/internal/store"
)

// Target is a parsed Redis connection string.
type Target struct {
	TLS      bool
	User     string
	Password string
	Host     string
	Port     int
	DB       int
}

func (t Target) addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// URI renders the target without its password.
func (t Target) URI() string {
	scheme := "redis"
	if t.TLS {
		scheme = "rediss"
	}
	u := url.URL{Scheme: scheme, Host: t.addr(), Path: "/" + strconv.Itoa(t.DB)}
	if t.User != "" {
		u.User = url.User(t.User)
	}
	return u.String()
}

func isAzure(host string) bool {
	h := strings.ToLower(host)
	return strings.HasSuffix(h, ".redis.cache.windows.net") || strings.HasSuffix(h, ".redisenterprise.cache.azure.net") ||
		strings.HasSuffix(h, ".redis.azure.net")
}

// ParseTarget accepts redis:// and rediss:// URLs, "host[:port]", and the
// StackExchange.Redis form shown in the Azure portal:
// "name.redis.cache.windows.net:6380,password=...,ssl=True,abortConnect=False".
func ParseTarget(s string) (Target, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Target{}, errors.New("connection string is required")
	}
	var t Target
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "redis://") || strings.HasPrefix(lower, "rediss://"):
		u, err := url.Parse(s)
		if err != nil {
			return t, fmt.Errorf("invalid connection string: %w", err)
		}
		t.TLS = strings.EqualFold(u.Scheme, "rediss")
		t.Host = u.Hostname()
		if p := u.Port(); p != "" {
			if t.Port, err = strconv.Atoi(p); err != nil {
				return t, fmt.Errorf("invalid port %q", p)
			}
		}
		if u.User != nil {
			t.User = u.User.Username()
			t.Password, _ = u.User.Password()
			// redis://:password@host has an empty user name
		}
		if db := strings.Trim(u.Path, "/"); db != "" {
			n, err := strconv.Atoi(db)
			if err != nil || n < 0 {
				return t, fmt.Errorf("invalid database number %q", db)
			}
			t.DB = n
		}
		if q := u.Query().Get("db"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n >= 0 {
				t.DB = n
			}
		}
	case strings.Contains(s, "://"):
		return t, errors.New("connection string must start with redis:// or rediss://")
	default:
		parts := strings.Split(s, ",")
		hostport := strings.TrimSpace(parts[0])
		host, port, err := net.SplitHostPort(hostport)
		if err != nil {
			host = hostport
		} else if t.Port, err = strconv.Atoi(port); err != nil {
			return t, fmt.Errorf("invalid port %q", port)
		}
		t.Host = host
		if isAzure(host) {
			t.TLS = true
		}
		for _, kv := range parts[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "password":
				t.Password = strings.TrimSpace(v)
			case "user", "username":
				t.User = strings.TrimSpace(v)
			case "ssl", "tls":
				t.TLS = strings.EqualFold(strings.TrimSpace(v), "true")
			case "defaultdatabase":
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
					t.DB = n
				}
			}
		}
	}
	if t.Host == "" {
		return t, errors.New("connection string has no host")
	}
	if t.Port == 0 {
		t.Port = 6379
		if isAzure(t.Host) && t.TLS {
			t.Port = 6380
		}
	}
	return t, nil
}

// DBName and ParseDBName convert between 3 and "db3".
func DBName(n int) string { return "db" + strconv.Itoa(n) }

func ParseDBName(s string) (int, bool) {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "db")
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0
}

// Normalize prepares a Redis profile for saving: a password in the
// connection string moves to Password (the Keychain), and the stored URI
// never contains it.
func Normalize(c store.Connection) (store.Connection, error) {
	t, err := ParseTarget(c.URI)
	if err != nil {
		return c, err
	}
	if t.Password != "" && c.Password == "" {
		c.Password = t.Password
	}
	if n, ok := ParseDBName(c.Database); ok && c.Database != "" {
		t.DB = n
	}
	c.URI = t.URI()
	c.Server = t.addr()
	c.User = t.User
	c.Database = DBName(t.DB)
	c.Auth = ""
	if c.Name == "" {
		c.Name = t.Host
	}
	return c, nil
}

func options(c store.Connection, db int) (*redis.Options, error) {
	t, err := ParseTarget(c.URI)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(c.ConnectTimeout) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	o := &redis.Options{
		Addr:                  t.addr(),
		Username:              t.User,
		Password:              c.Password,
		DB:                    db,
		DialTimeout:           timeout,
		ReadTimeout:           -1, // no read deadline: long commands are cancelled via context
		WriteTimeout:          30 * time.Second,
		ClientName:            "SQL-Studio",
		DisableIdentity:       true, // CLIENT SETINFO is not supported everywhere (e.g. older Azure caches)
		PoolSize:              4,
		MaxRetries:            1,
		ContextTimeoutEnabled: true,
	}
	if t.TLS {
		o.TLSConfig = &tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12}
	}
	return o, nil
}

// Manager owns Redis clients: one per connection profile and database.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]*redis.Client
	profiles map[string]store.Connection
}

func NewManager() *Manager {
	return &Manager{clients: map[string]*redis.Client{}, profiles: map[string]store.Connection{}}
}

func key(id string, db int) string { return id + "\x00" + strconv.Itoa(db) }

// Connect verifies the profile with PING and keeps it for later use.
func (m *Manager) Connect(ctx context.Context, c store.Connection) (*redis.Client, error) {
	m.Disconnect(c.ID)
	db, _ := ParseDBName(c.Database)
	m.mu.Lock()
	m.profiles[c.ID] = c
	m.mu.Unlock()
	cl, err := m.Client(ctx, c.ID, db)
	if err != nil {
		m.Disconnect(c.ID)
		return nil, err
	}
	return cl, nil
}

// Client returns (creating if needed) the client for a database.
func (m *Manager) Client(ctx context.Context, id string, db int) (*redis.Client, error) {
	m.mu.Lock()
	c, ok := m.profiles[id]
	if !ok {
		m.mu.Unlock()
		return nil, errors.New("not connected")
	}
	if cl, ok := m.clients[key(id, db)]; ok {
		m.mu.Unlock()
		return cl, nil
	}
	m.mu.Unlock()
	o, err := options(c, db)
	if err != nil {
		return nil, err
	}
	cl := redis.NewClient(o)
	if err := cl.Ping(ctx).Err(); err != nil {
		_ = cl.Close()
		return nil, friendlyError(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.clients[key(id, db)]; ok {
		_ = cl.Close()
		return existing, nil
	}
	m.clients[key(id, db)] = cl
	return cl, nil
}

// Profile returns the registered profile.
func (m *Manager) Profile(id string) (store.Connection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.profiles[id]
	return c, ok
}

// Disconnect closes all clients of a profile.
func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	var cls []*redis.Client
	for k, cl := range m.clients {
		if strings.HasPrefix(k, id+"\x00") {
			cls = append(cls, cl)
			delete(m.clients, k)
		}
	}
	delete(m.profiles, id)
	m.mu.Unlock()
	for _, cl := range cls {
		_ = cl.Close()
	}
}

// Close disconnects everything.
func (m *Manager) Close() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.profiles))
	for id := range m.profiles {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Disconnect(id)
	}
}

func infoField(info, field string) string {
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), field+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Info describes the server.
func Info(ctx context.Context, cl *redis.Client, c store.Connection) engine.ServerInfo {
	si := engine.ServerInfo{Edition: "Redis", Login: c.User, Database: c.Database}
	if info, err := cl.Info(ctx, "server").Result(); err == nil {
		si.Version = infoField(info, "redis_version")
		if v := infoField(info, "valkey_version"); v != "" {
			si.Edition, si.Version = "Valkey", v
		}
	}
	host := strings.ToLower(c.Server)
	if isAzure(strings.Split(host, ":")[0]) {
		si.Edition, si.IsAzure = "Azure Cache for Redis", true
	}
	return si
}

// Test connects with a throwaway client.
func Test(ctx context.Context, c store.Connection) (engine.ServerInfo, error) {
	db, _ := ParseDBName(c.Database)
	o, err := options(c, db)
	if err != nil {
		return engine.ServerInfo{}, err
	}
	cl := redis.NewClient(o)
	defer cl.Close()
	if err := cl.Ping(ctx).Err(); err != nil {
		return engine.ServerInfo{}, friendlyError(err)
	}
	return Info(ctx, cl, c), nil
}

// Databases lists databases that hold keys, plus the profile's database.
func Databases(ctx context.Context, cl *redis.Client, c store.Connection) ([]string, error) {
	seen := map[int]bool{}
	if n, ok := ParseDBName(c.Database); ok {
		seen[n] = true
	}
	info, err := cl.Info(ctx, "keyspace").Result()
	if err != nil {
		return nil, friendlyError(err)
	}
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "db") {
			continue
		}
		name, _, _ := strings.Cut(line, ":")
		if n, ok := ParseDBName(name); ok {
			seen[n] = true
		}
	}
	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]string, len(nums))
	for i, n := range nums {
		out[i] = DBName(n)
	}
	return out, nil
}

// KeyLimit caps the keys listed in the explorer.
const KeyLimit = 2000

// Keys lists up to KeyLimit keys with their types (SCAN + pipelined TYPE).
// When more keys exist, a final entry with Kind "more" is appended.
func Keys(ctx context.Context, cl *redis.Client, pattern string) ([]engine.DbObject, error) {
	if pattern == "" {
		pattern = "*"
	}
	var keys []string
	var cursor uint64
	for {
		batch, next, err := cl.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			return nil, friendlyError(err)
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 || len(keys) > KeyLimit {
			break
		}
	}
	more := len(keys) > KeyLimit || cursor != 0
	if len(keys) > KeyLimit {
		keys = keys[:KeyLimit]
	}
	sort.Strings(keys)
	pipe := cl.Pipeline()
	cmds := make([]*redis.StatusCmd, len(keys))
	for i, k := range keys {
		cmds[i] = pipe.Type(ctx, k)
	}
	if len(keys) > 0 {
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return nil, friendlyError(err)
		}
	}
	out := make([]engine.DbObject, 0, len(keys)+1)
	for i, k := range keys {
		t := cmds[i].Val()
		if t == "none" || t == "" {
			continue // expired between SCAN and TYPE
		}
		out = append(out, engine.DbObject{Name: k, Kind: t})
	}
	if more {
		out = append(out, engine.DbObject{Name: fmt.Sprintf("first %d keys", KeyLimit), Kind: "more"})
	}
	return out, nil
}

// SampleKeys returns some key names for autocompletion.
func SampleKeys(ctx context.Context, cl *redis.Client, n int) []string {
	keys, _, err := cl.Scan(ctx, 0, "*", int64(n)).Result()
	if err != nil {
		return nil
	}
	sort.Strings(keys)
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

func friendlyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var hint string
	switch {
	case strings.Contains(msg, "WRONGPASS") || strings.Contains(msg, "invalid password") || strings.Contains(msg, "NOAUTH"):
		hint = "Check the password (for Azure Cache for Redis, use one of the access keys)."
	case strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host"):
		hint = "Check the host and port and that your network can reach them. Azure Cache for Redis uses TLS on port 6380 (rediss://)."
	case strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset"):
		hint = "The server closed the connection. If it requires TLS, use rediss:// (Azure Cache for Redis: port 6380)."
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
		hint = "The server certificate could not be verified."
	}
	if hint != "" {
		return fmt.Errorf("%s\n\n%s", msg, hint)
	}
	return err
}
