# SQL Studio

A lightweight, native-feeling SQL Server / **Azure SQL** client for macOS — a fast replacement for Azure Data Studio.

- **~15 MB app**, starts in under a second (uses the macOS system WebKit, not Electron)
- First-class **Microsoft Entra ID** sign-in: interactive browser (MFA), `az login`, device code, service principal, default credential chain — plus classic SQL logins
- Passwords and client secrets are stored in the **macOS Keychain**, never on disk
- Works with Azure SQL Database, Azure SQL Managed Instance, Synapse, Fabric SQL endpoints and SQL Server 2012+

## Features

| | |
|---|---|
| **Query editor** | T-SQL syntax highlighting, autocomplete for keywords, tables and columns, multiple tabs, find/replace, multi-cursor, formatter (⌥⇧F), open/save `.sql` files, tabs restored on relaunch |
| **Execution** | Runs selection, current statement (⇧⌘E) or whole script; `GO` / `GO n` batch separators; cancel (⌘.); optional timeout. Each tab keeps its own connection, so `#temp` tables, `SET` options and transactions persist like in SSMS/ADS |
| **Results** | Virtualized grid (100k+ rows), multiple result sets, sort, resize, rectangular selection with sum/avg, copy as TSV/CSV/JSON/Markdown/INSERT, export CSV/JSON, cell viewer with JSON/XML pretty-print |
| **Messages** | `PRINT` output, rows affected, SSMS-style errors (`Msg 208, Level 16, State 1, Line 3`) — click to jump to the line |
| **Object explorer** | Databases → tables, views, procedures, functions → columns (types, PK, identity). Filter, Select Top 1000, Count Rows, Script as CREATE/ALTER/EXECUTE, **Script Data as INSERT** (TOP, WHERE filter, ORDER BY, target table, identity values, optional CREATE TABLE) to a new query, the clipboard or a file |
| **Connections** | Groups, colors (tab accent), paste an ADO.NET connection string from the Azure portal, test before saving, read-only intent |
| **Other** | Query history (⌘Y), light/dark/system theme, zoom, keyboard-first |

## Install

### Homebrew (recommended)

```sh
brew tap thennavanr/sql-studio https://github.com/thennavanr/sql-studio
brew install --cask --appdir ~/Applications thennavanr/sql-studio/sql-studio   # no admin rights needed
open ~/Applications/"SQL Studio.app"
```

Upgrade with `brew upgrade --cask sql-studio`; uninstall with `brew uninstall --cask sql-studio` (add `--zap` to also remove saved connections and settings). Drop `--appdir ~/Applications` to install into `/Applications` instead.

The cask clears macOS's download quarantine, because the app is ad-hoc signed but not notarized.

If you installed SQL Studio manually before, remove that copy first so brew can install its own: `rm -rf ~/Applications/"SQL Studio.app"`. Saved connections and Keychain passwords are kept.

If the install fails, run it again with `--verbose` and include the output when reporting the problem.

### Manual

Download `SQL-Studio-macOS-universal.dmg` from [Releases](../../releases). If you can write to `/Applications`, drag **SQL Studio** there; otherwise copy it into your own Applications folder. Then clear the quarantine flag, which otherwise makes macOS say the app is "damaged":

```sh
mkdir -p ~/Applications
cp -R "/Volumes/SQL Studio/SQL Studio.app" ~/Applications/
xattr -dr com.apple.quarantine ~/Applications/"SQL Studio.app"
open ~/Applications/"SQL Studio.app"
```

## Connecting to Azure SQL

1. **New Connection** (⇧⌘N) → Server: `yourserver.database.windows.net` (or paste the connection string from *Azure portal → SQL database → Connection strings*).
2. Pick an authentication method:

| Method | When to use |
|---|---|
| **Entra ID – Universal with MFA** | Default. Same as "Azure Active Directory – Universal with MFA" in SSMS/ADS: opens your browser, supports MFA/Conditional Access. Optional login hint and tenant. |
| **Entra ID – Azure CLI** | You already use `az login` (`brew install azure-cli`). |
| **Entra ID – Device code** | Browser redirect is blocked; sign in from any device. |
| **Entra ID – Service principal** | Automation / app registrations (client ID + secret + tenant). |
| **Entra ID – Default** | Environment variables, workload/managed identity, az / azd CLI. |
| **SQL Server authentication** | SQL logins (e.g. `sqladmin`). |

Common errors are explained in the app — e.g. *“Client with IP address … is not allowed”* → add your IP under the server's **Networking** firewall rules; *“Login failed for user '&lt;token-identified principal&gt;'”* → your Entra user needs `CREATE USER [you@company.com] FROM EXTERNAL PROVIDER;` in that database.

Tokens are cached in memory per connection, so one browser sign-in covers every database on that server until you quit.

## Keyboard shortcuts

| Shortcut | Action |
|---|---|
| ⌘E / ⌘↵ / F5 | Execute selection or whole script |
| ⇧⌘E / ⇧⌘↵ | Execute current statement (block around cursor) |
| ⌘. | Cancel |
| ⌘N / ⌘W | New / close query tab |
| ⇧⌘] / ⇧⌘[ | Next / previous tab |
| ⌘O / ⌘S / ⇧⌘S | Open / save / save as |
| ⌥⇧F | Format SQL |
| ⌘R | Toggle results pane |
| ⌘B | Toggle sidebar |
| ⌘Y | Query history |
| ⌘, | Settings |
| ⌘= / ⌘- / ⌘0 | Zoom |

In the grid: click / shift-click / drag to select, ⌘A select all, ⌘C copy, ⇧⌘C copy with headers, double-click or ↵ to view a cell.

## Building from source

Requirements: macOS 11+, Go 1.26+, Node 20+, Xcode command line tools.

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2
make dev               # hot-reload development
make build             # build/bin/SQL Studio.app for this Mac
make build-universal   # Apple Silicon + Intel
```

Releases: run the **Build** workflow manually (Actions → Build → *Run workflow*) with a version such as `0.2.0`, or push a `v0.2.0` tag. Either way it tests, builds the universal app, publishes a GitHub Release with the `.dmg` and `.zip`, and updates the Homebrew cask in `Casks/sql-studio.rb` on `main`.

Tests (the integration test needs a SQL Server, e.g. in Docker):

```sh
docker run -d -e ACCEPT_EULA=Y -e 'MSSQL_SA_PASSWORD=Str0ng!Passw0rd' -p 1433:1433 mcr.microsoft.com/mssql/server:2022-latest
make test-integration
```

## Architecture

```
main.go / app.go          Wails app, native menu, bindings exposed to the UI
internal/engine           go-mssqldb: auth (azidentity), pools, per-tab sessions,
                          GO batch splitting, streaming execution, metadata queries
internal/store            connection profiles (JSON) + Keychain secrets, settings, history
frontend/                 TypeScript + Vite, CodeMirror 6 editor, custom virtualized grid
```

Results stream from Go to the UI in chunks as events, so large queries render progressively and can be cancelled at any time. Settings live in `~/Library/Application Support/SQL Studio/`.

## License

MIT
