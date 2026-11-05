package mongo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/thennavanr/sql-studio/internal/engine"
	"github.com/thennavanr/sql-studio/internal/store"
)

// uriParts is a parsed MongoDB connection string.
type uriParts struct {
	scheme   string // mongodb or mongodb+srv
	user     string // decoded
	password string // decoded
	hasPass  bool
	hosts    string // host list as written, e.g. a:27017,b:27017
	rest     string // "/db?opts" (may be empty)
}

func parseURI(uri string) (uriParts, error) {
	var u uriParts
	s := strings.TrimSpace(uri)
	i := strings.Index(s, "://")
	if i < 0 {
		return u, errors.New("connection string must start with mongodb:// or mongodb+srv://")
	}
	u.scheme = strings.ToLower(s[:i])
	if u.scheme != "mongodb" && u.scheme != "mongodb+srv" {
		return u, errors.New("connection string must start with mongodb:// or mongodb+srv://")
	}
	s = s[i+3:]
	end := strings.IndexAny(s, "/?")
	if end < 0 {
		end = len(s)
	}
	authority := s[:end]
	u.rest = s[end:]
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		info := authority[:at]
		authority = authority[at+1:]
		user, pass, has := strings.Cut(info, ":")
		var err error
		if u.user, err = url.PathUnescape(user); err != nil {
			return u, fmt.Errorf("invalid user name encoding in connection string")
		}
		if has {
			u.hasPass = true
			if u.password, err = url.PathUnescape(pass); err != nil {
				return u, fmt.Errorf("invalid password encoding in connection string")
			}
		}
	}
	if authority == "" {
		return u, errors.New("connection string has no host")
	}
	u.hosts = authority
	return u, nil
}

// escapeUserInfo percent-encodes everything but RFC 3986 unreserved characters.
func escapeUserInfo(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

func (u uriParts) build(withPassword bool) string {
	var sb strings.Builder
	sb.WriteString(u.scheme + "://")
	if u.user != "" {
		sb.WriteString(escapeUserInfo(u.user))
		if withPassword && u.password != "" {
			sb.WriteString(":" + escapeUserInfo(u.password))
		}
		sb.WriteByte('@')
	}
	sb.WriteString(u.hosts)
	sb.WriteString(u.rest)
	return sb.String()
}

// defaultDB returns the database named in the URI path, if any.
func (u uriParts) defaultDB() string {
	if !strings.HasPrefix(u.rest, "/") {
		return ""
	}
	db := u.rest[1:]
	if q := strings.IndexByte(db, '?'); q >= 0 {
		db = db[:q]
	}
	db, _ = url.PathUnescape(db)
	return db
}

// Normalize prepares a MongoDB profile for saving: a password embedded in the
// URI is moved into Password (so it goes to the Keychain, never to disk), and
// User/Server/Database are filled from the URI for display.
func Normalize(c store.Connection) (store.Connection, error) {
	u, err := parseURI(c.URI)
	if err != nil {
		return c, err
	}
	if u.hasPass && u.password != "" && c.Password == "" {
		c.Password = u.password
	}
	u.password = ""
	c.URI = u.build(false)
	c.User = u.user
	c.Server = u.hosts
	if c.Database == "" {
		c.Database = u.defaultDB()
	}
	c.Auth = ""
	if c.Name == "" {
		c.Name = c.Server
		if c.Database != "" {
			c.Name += " / " + c.Database
		}
	}
	return c, nil
}

// NeedsPassword reports whether the URI names a user (so a password is expected).
func NeedsPassword(c store.Connection) bool {
	u, err := parseURI(c.URI)
	if err != nil || u.user == "" {
		return false
	}
	q := strings.ToLower(u.rest)
	// These mechanisms do not use a password.
	for _, m := range []string{"mongodb-x509", "mongodb-aws", "mongodb-oidc", "gssapi"} {
		if strings.Contains(q, "authmechanism="+m) {
			return false
		}
	}
	return true
}

// Manager owns MongoDB clients, one per connection profile.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]*mongo.Client
	profiles map[string]store.Connection
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{clients: map[string]*mongo.Client{}, profiles: map[string]store.Connection{}}
}

func clientOptions(c store.Connection) (*options.ClientOptions, error) {
	u, err := parseURI(c.URI)
	if err != nil {
		return nil, err
	}
	if c.Password != "" {
		u.password = c.Password
	}
	timeout := time.Duration(c.ConnectTimeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	opts := options.Client().ApplyURI(u.build(true)).SetAppName("SQL Studio").
		SetServerSelectionTimeout(timeout).SetConnectTimeout(timeout)
	if err := opts.Validate(); err != nil {
		return nil, friendlyError(err)
	}
	return opts, nil
}

// Connect opens (or reuses) a client for the profile and verifies it with a ping.
func (m *Manager) Connect(ctx context.Context, c store.Connection) (*mongo.Client, error) {
	m.mu.Lock()
	if cl, ok := m.clients[c.ID]; ok && m.profiles[c.ID] == c {
		m.mu.Unlock()
		return cl, nil
	}
	m.mu.Unlock()
	m.Disconnect(c.ID)

	opts, err := clientOptions(c)
	if err != nil {
		return nil, err
	}
	cl, err := mongo.Connect(opts)
	if err != nil {
		return nil, friendlyError(err)
	}
	if err := cl.Ping(ctx, nil); err != nil {
		_ = cl.Disconnect(context.Background())
		return nil, friendlyError(err)
	}
	m.mu.Lock()
	m.clients[c.ID] = cl
	m.profiles[c.ID] = c
	m.mu.Unlock()
	return cl, nil
}

// Client returns the connected client for a profile ID.
func (m *Manager) Client(id string) (*mongo.Client, store.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cl, ok := m.clients[id]
	if !ok {
		return nil, store.Connection{}, errors.New("not connected")
	}
	return cl, m.profiles[id], nil
}

// Disconnect closes a profile's client.
func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	cl := m.clients[id]
	delete(m.clients, id)
	delete(m.profiles, id)
	m.mu.Unlock()
	if cl != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cl.Disconnect(ctx)
	}
}

// Close disconnects everything.
func (m *Manager) Close() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Disconnect(id)
	}
}

// Info describes the server.
func Info(ctx context.Context, cl *mongo.Client, c store.Connection) engine.ServerInfo {
	si := engine.ServerInfo{Edition: "MongoDB", Login: c.User, Database: c.Database}
	var bi struct {
		Version string `bson:"version"`
	}
	if err := cl.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi); err == nil {
		si.Version = bi.Version
	}
	host := strings.ToLower(c.Server)
	switch {
	case strings.Contains(host, ".cosmos.azure.com") || strings.Contains(host, ".mongocluster.cosmos"):
		si.Edition = "Azure Cosmos DB for MongoDB"
		si.IsAzure = true
	case strings.Contains(host, ".mongodb.net"):
		si.Edition = "MongoDB Atlas"
	}
	return si
}

// Test connects with a throwaway client.
func Test(ctx context.Context, c store.Connection) (engine.ServerInfo, error) {
	opts, err := clientOptions(c)
	if err != nil {
		return engine.ServerInfo{}, err
	}
	cl, err := mongo.Connect(opts)
	if err != nil {
		return engine.ServerInfo{}, friendlyError(err)
	}
	defer cl.Disconnect(context.Background())
	if err := cl.Ping(ctx, nil); err != nil {
		return engine.ServerInfo{}, friendlyError(err)
	}
	return Info(ctx, cl, c), nil
}

// Databases lists database names. Users without listDatabases permission
// still see their default database.
func Databases(ctx context.Context, cl *mongo.Client, c store.Connection) ([]string, error) {
	names, err := cl.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		if c.Database != "" {
			return []string{c.Database}, nil
		}
		return nil, friendlyError(err)
	}
	if c.Database != "" && !contains(names, c.Database) {
		names = append(names, c.Database)
	}
	// User databases first, then admin/config/local.
	sort.SliceStable(names, func(i, j int) bool {
		si, sj := isSystemDB(names[i]), isSystemDB(names[j])
		if si != sj {
			return !si
		}
		return names[i] < names[j]
	})
	return names, nil
}

func isSystemDB(n string) bool { return n == "admin" || n == "config" || n == "local" }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Collections lists collections and views as explorer objects.
func Collections(ctx context.Context, cl *mongo.Client, db string) ([]engine.DbObject, error) {
	cur, err := cl.Database(db).ListCollections(ctx, bson.D{}, options.ListCollections().SetNameOnly(true).SetAuthorizedCollections(true))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer cur.Close(ctx)
	out := []engine.DbObject{}
	for cur.Next(ctx) {
		var info struct {
			Name string `bson:"name"`
			Type string `bson:"type"`
		}
		if err := cur.Decode(&info); err != nil {
			return nil, err
		}
		if strings.HasPrefix(info.Name, "system.") {
			continue // internal collections (system.views, system.profile, ...); still queryable
		}
		kind := "collection"
		switch info.Type {
		case "view":
			kind = "view"
		case "timeseries":
			kind = "collection"
		}
		out = append(out, engine.DbObject{Name: info.Name, Kind: kind})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, cur.Err()
}

// SampleFields returns field names seen in a sample of documents, for autocompletion.
func SampleFields(ctx context.Context, cl *mongo.Client, db, coll string) ([]string, error) {
	cur, err := cl.Database(db).Collection(coll).Find(ctx, bson.D{}, options.Find().SetLimit(50))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	seen := map[string]bool{}
	var out []string
	for cur.Next(ctx) {
		elems, err := cur.Current.Elements()
		if err != nil {
			continue
		}
		for _, e := range elems {
			if k := e.Key(); !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out, cur.Err()
}

// ---- single-document operations used by the Documents view ----

// InsertDocument inserts a document given in shell syntax and returns its _id.
func InsertDocument(ctx context.Context, cl *mongo.Client, db, coll, docText string) (string, error) {
	doc, err := ParseDocument(docText)
	if err != nil {
		return "", err
	}
	res, err := cl.Database(db).Collection(coll).InsertOne(ctx, doc)
	if err != nil {
		return "", friendlyError(err)
	}
	return formatAny(res.InsertedID), nil
}

// ReplaceDocument replaces the document with the given _id (shell syntax) and
// returns the stored document, re-read from the server.
func ReplaceDocument(ctx context.Context, cl *mongo.Client, db, coll, idText, docText string) (string, error) {
	id, err := ParseValue(idText)
	if err != nil {
		return "", fmt.Errorf("_id: %w", err)
	}
	doc, err := ParseDocument(docText)
	if err != nil {
		return "", err
	}
	// The _id cannot change; drop it from the replacement if it is unchanged
	// and refuse an attempted change with a clear message.
	for i, e := range doc {
		if e.Key == "_id" {
			a, _ := bson.Marshal(bson.D{{Key: "v", Value: e.Value}})
			b, _ := bson.Marshal(bson.D{{Key: "v", Value: id}})
			if string(a) != string(b) {
				return "", errors.New("_id cannot be changed; insert a new document instead")
			}
			doc = append(doc[:i:i], doc[i+1:]...)
			break
		}
	}
	c := cl.Database(db).Collection(coll)
	res, err := c.ReplaceOne(ctx, bson.D{{Key: "_id", Value: id}}, doc)
	if err != nil {
		return "", friendlyError(err)
	}
	if res.MatchedCount == 0 {
		return "", errors.New("document not found (it may have been deleted)")
	}
	raw, err := c.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Raw()
	if err != nil {
		return "", friendlyError(err)
	}
	return FormatDoc(raw, true), nil
}

// DeleteDocument deletes the document with the given _id.
func DeleteDocument(ctx context.Context, cl *mongo.Client, db, coll, idText string) error {
	id, err := ParseValue(idText)
	if err != nil {
		return fmt.Errorf("_id: %w", err)
	}
	res, err := cl.Database(db).Collection(coll).DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return friendlyError(err)
	}
	if res.DeletedCount == 0 {
		return errors.New("document not found (it may already have been deleted)")
	}
	return nil
}

// formatAny prints a Go value (as returned by the driver) in shell syntax.
func formatAny(v any) string {
	b, err := bson.Marshal(bson.D{{Key: "v", Value: v}})
	if err != nil {
		return fmt.Sprint(v)
	}
	return FormatValue(bson.Raw(b).Lookup("v"), false)
}

// friendlyError adds hints for common connection problems.
func friendlyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var hint string
	switch {
	case strings.Contains(msg, "server selection timeout") || strings.Contains(msg, "server selection error"):
		hint = "Could not reach the server. Check the host name, that the port is open from your network, and (for Atlas / Cosmos DB) that your IP address is allowed in the network access list."
	case strings.Contains(msg, "Authentication failed") || strings.Contains(msg, "auth error"):
		hint = "Check the user name and password. If the user is defined in another database, add ?authSource=<db> (often admin) to the connection string."
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup "):
		hint = "The host name could not be resolved. For mongodb+srv:// connection strings, check the cluster address."
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
		hint = "The server certificate could not be verified. For local test servers you can add tlsAllowInvalidCertificates=true to the connection string."
	}
	if hint != "" {
		return fmt.Errorf("%s\n\n%s", msg, hint)
	}
	return err
}
