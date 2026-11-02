// Package engine talks to SQL Server / Azure SQL: authentication, connection
// pooling, per-editor sessions, query execution and metadata.
package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	mssql "github.com/microsoft/go-mssqldb"

	"github.com/thennavanr/sql-studio/internal/store"
)

// azureSQLScope is the OAuth scope for Azure SQL Database / Managed Instance / Synapse / Fabric.
const azureSQLScope = "https://database.windows.net/.default"

// DeviceCodeFunc is invoked when device-code sign-in needs the user to visit a URL.
type DeviceCodeFunc func(connectionID, message, userCode, verificationURL string)

// Manager owns credentials, connection pools and editor sessions.
type Manager struct {
	mu       sync.Mutex
	creds    map[string]azcore.TokenCredential // connection ID -> Entra credential (caches tokens)
	profiles map[string]store.Connection       // connection ID -> profile incl. in-memory secret
	pools    map[string]*sql.DB                // connection ID + "\x00" + database -> pool
	sessions map[string]*Session               // editor tab ID -> session

	OnDeviceCode DeviceCodeFunc
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{
		creds:    map[string]azcore.TokenCredential{},
		profiles: map[string]store.Connection{},
		pools:    map[string]*sql.DB{},
		sessions: map[string]*Session{},
	}
}

// Register makes a connection profile (with secret) available for pools and sessions.
// Re-registering a changed profile drops existing pools, credentials and sessions.
func (m *Manager) Register(c store.Connection) {
	m.mu.Lock()
	old, ok := m.profiles[c.ID]
	m.mu.Unlock()
	if ok && old != c {
		m.Disconnect(c.ID)
	}
	m.mu.Lock()
	m.profiles[c.ID] = c
	m.mu.Unlock()
}

// IsRegistered reports whether the connection has been registered (i.e. is "connected").
func (m *Manager) IsRegistered(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.profiles[id]
	return ok
}

// Disconnect closes everything associated with a connection.
func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	var pools []*sql.DB
	var sessions []*Session
	for k, p := range m.pools {
		if strings.HasPrefix(k, id+"\x00") {
			pools = append(pools, p)
			delete(m.pools, k)
		}
	}
	for k, s := range m.sessions {
		if s.ConnectionID == id {
			sessions = append(sessions, s)
			delete(m.sessions, k)
		}
	}
	delete(m.creds, id)
	delete(m.profiles, id)
	m.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
	for _, p := range pools {
		_ = p.Close()
	}
}

// Close shuts everything down.
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

// Pool returns (creating if needed) a connection pool for the given connection and database.
// An empty database means the profile's default database.
func (m *Manager) Pool(ctx context.Context, connID, database string) (*sql.DB, error) {
	m.mu.Lock()
	c, ok := m.profiles[connID]
	if !ok {
		m.mu.Unlock()
		return nil, errors.New("not connected")
	}
	if database == "" {
		database = c.Database
	}
	key := connID + "\x00" + strings.ToLower(database)
	if p, ok := m.pools[key]; ok {
		m.mu.Unlock()
		return p, nil
	}
	m.mu.Unlock()

	connector, err := m.connector(c, database)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(10 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, friendlyError(err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[key]; ok { // lost a race; keep the first one
		_ = db.Close()
		return p, nil
	}
	m.pools[key] = db
	return db, nil
}

func (m *Manager) connector(c store.Connection, database string) (*mssql.Connector, error) {
	dsn, err := BuildDSN(c, database)
	if err != nil {
		return nil, err
	}
	if c.Auth == store.AuthSQL || c.Auth == "" {
		return mssql.NewConnector(dsn)
	}
	cred, err := m.credential(c)
	if err != nil {
		return nil, err
	}
	return mssql.NewConnectorWithAccessTokenProvider(dsn, func(ctx context.Context) (string, error) {
		tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{azureSQLScope}})
		if err != nil {
			return "", err
		}
		return tok.Token, nil
	})
}

// credential returns a cached Entra credential for the profile so a single
// browser sign-in is shared by every pool/database of that connection.
func (m *Manager) credential(c store.Connection) (azcore.TokenCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cred, ok := m.creds[c.ID]; ok {
		return cred, nil
	}
	var (
		cred azcore.TokenCredential
		err  error
	)
	switch c.Auth {
	case store.AuthEntraInteractive:
		cred, err = azidentity.NewInteractiveBrowserCredential(&azidentity.InteractiveBrowserCredentialOptions{
			ClientID:  c.ClientID, // empty = Azure SDK's default public client
			TenantID:  c.TenantID,
			LoginHint: c.User,
		})
	case store.AuthEntraAzCli:
		cred, err = azidentity.NewAzureCLICredential(&azidentity.AzureCLICredentialOptions{TenantID: c.TenantID})
	case store.AuthEntraDefault:
		cred, err = azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{TenantID: c.TenantID})
	case store.AuthEntraSP:
		if c.TenantID == "" {
			return nil, errors.New("tenant ID is required for service principal authentication")
		}
		cred, err = azidentity.NewClientSecretCredential(c.TenantID, c.User, c.Password, nil)
	case store.AuthEntraDeviceCode:
		id := c.ID
		cred, err = azidentity.NewDeviceCodeCredential(&azidentity.DeviceCodeCredentialOptions{
			ClientID: c.ClientID,
			TenantID: c.TenantID,
			UserPrompt: func(ctx context.Context, msg azidentity.DeviceCodeMessage) error {
				if m.OnDeviceCode != nil {
					m.OnDeviceCode(id, msg.Message, msg.UserCode, msg.VerificationURL)
				}
				return nil
			},
		})
	default:
		return nil, fmt.Errorf("unsupported authentication type %q", c.Auth)
	}
	if err != nil {
		return nil, err
	}
	m.creds[c.ID] = cred
	return cred, nil
}

// ParseServer accepts the forms users paste from the Azure portal or SSMS:
// "tcp:host,1433", "host,1433", "host:1433", "host\instance".
func ParseServer(server string, port int) (host string, p int, instance string) {
	s := strings.TrimSpace(server)
	if len(s) > 4 && strings.EqualFold(s[:4], "tcp:") {
		s = s[4:]
	}
	p = port
	if i := strings.LastIndex(s, ","); i >= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(s[i+1:])); err == nil {
			p = n
		}
		s = s[:i]
	} else if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[:i], ":") {
		if n, err := strconv.Atoi(s[i+1:]); err == nil {
			p = n
			s = s[:i]
		}
	}
	if i := strings.Index(s, `\`); i >= 0 {
		instance = s[i+1:]
		s = s[:i]
	}
	return strings.TrimSpace(s), p, instance
}

// IsAzureHost reports whether the host looks like an Azure SQL endpoint.
func IsAzureHost(host string) bool {
	h := strings.ToLower(host)
	for _, suffix := range []string{
		".database.windows.net", ".database.azure.com", ".database.usgovcloudapi.net",
		".database.chinacloudapi.cn", ".sql.azuresynapse.net", ".datawarehouse.fabric.microsoft.com",
		".database.fabric.microsoft.com",
	} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// BuildDSN builds a go-mssqldb URL connection string for a profile.
func BuildDSN(c store.Connection, database string) (string, error) {
	host, port, instance := ParseServer(c.Server, c.Port)
	if host == "" {
		return "", errors.New("server is required")
	}
	q := url.Values{}
	if database != "" {
		q.Set("database", database)
	}
	enc := c.Encrypt
	if enc == "" {
		enc = "true"
	}
	q.Set("encrypt", enc)
	if c.TrustServerCertificate {
		q.Set("TrustServerCertificate", "true")
	}
	timeout := c.ConnectTimeout
	if timeout <= 0 {
		timeout = 30
	}
	q.Set("connection timeout", strconv.Itoa(timeout))
	q.Set("dial timeout", strconv.Itoa(timeout))
	q.Set("app name", "SQL Studio")
	if c.ApplicationIntent != "" {
		q.Set("ApplicationIntent", c.ApplicationIntent)
	}
	u := &url.URL{Scheme: "sqlserver", Host: host, RawQuery: q.Encode()}
	if port > 0 {
		u.Host = host + ":" + strconv.Itoa(port)
	}
	if instance != "" {
		u.Path = "/" + instance
	}
	if c.Auth == store.AuthSQL || c.Auth == "" {
		u.User = url.UserPassword(c.User, c.Password)
	}
	return u.String(), nil
}

// ServerInfo summarises the server after connecting.
type ServerInfo struct {
	Version       string `json:"version"`
	ProductLevel  string `json:"productLevel"`
	Edition       string `json:"edition"`
	EngineEdition int    `json:"engineEdition"`
	IsAzure       bool   `json:"isAzure"`
	Login         string `json:"login"`
	Database      string `json:"database"`
}

// Info queries basic server details.
func Info(ctx context.Context, db *sql.DB) (ServerInfo, error) {
	var si ServerInfo
	err := db.QueryRowContext(ctx, `SELECT
		CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)),
		CAST(ISNULL(SERVERPROPERTY('ProductLevel'), '') AS nvarchar(128)),
		CAST(SERVERPROPERTY('Edition') AS nvarchar(128)),
		CAST(SERVERPROPERTY('EngineEdition') AS int),
		SUSER_SNAME(), DB_NAME()`).Scan(&si.Version, &si.ProductLevel, &si.Edition, &si.EngineEdition, &si.Login, &si.Database)
	// EngineEdition 5 = Azure SQL Database, 6 = Synapse, 8 = Managed Instance, 9 = Edge, 11 = Synapse serverless, 12 = Fabric
	switch si.EngineEdition {
	case 5, 6, 8, 11, 12:
		si.IsAzure = true
	}
	return si, friendlyError(err)
}

// Test opens a throwaway connection using the profile and returns server info.
func Test(ctx context.Context, c store.Connection, onDeviceCode DeviceCodeFunc) (ServerInfo, error) {
	m := NewManager()
	m.OnDeviceCode = onDeviceCode
	defer m.Close()
	m.Register(c)
	db, err := m.Pool(ctx, c.ID, c.Database)
	if err != nil {
		return ServerInfo{}, err
	}
	return Info(ctx, db)
}

// friendlyError adds hints for the most common Azure SQL connection problems.
func friendlyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var hint string
	switch {
	case strings.Contains(msg, "Client with IP address"):
		hint = "Add your IP address to the Azure SQL server firewall (Azure portal → SQL server → Networking)."
	case strings.Contains(msg, "Login failed for user '<token-identified principal>'"):
		hint = "Your Entra account was authenticated but has no user in this database. Ask an admin to run: CREATE USER [you@domain.com] FROM EXTERNAL PROVIDER;"
	case strings.Contains(msg, "Azure CLI not found") || strings.Contains(msg, "az: command not found") || strings.Contains(msg, "executable file not found"):
		hint = "Install the Azure CLI (brew install azure-cli) and run `az login`."
	case strings.Contains(msg, "Please run 'az login'") || strings.Contains(msg, "az login"):
		hint = "Run `az login` in Terminal, then try again."
	case strings.Contains(msg, "certificate") && strings.Contains(msg, "x509"):
		hint = "The server certificate could not be verified. For local/dev servers enable 'Trust server certificate'."
	case strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "no such host"):
		hint = "Check the server name and that port 1433 is reachable from your network."
	}
	if hint != "" {
		return fmt.Errorf("%s\n\n%s", msg, hint)
	}
	return err
}
