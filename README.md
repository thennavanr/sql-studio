# SQL Studio

A lightweight, native-feeling **SQL Server / Azure SQL** and **MongoDB** client for macOS — a fast replacement for Azure Data Studio and a light alternative to MongoDB Compass.

- **~15 MB app**, starts in under a second (uses the macOS system WebKit, not Electron)
- First-class **Microsoft Entra ID** sign-in: interactive browser (MFA), `az login`, device code, service principal, default credential chain — plus classic SQL logins
- Passwords and client secrets are stored in the **macOS Keychain**, never on disk
- Works with Azure SQL Database, Azure SQL Managed Instance, Synapse, Fabric SQL endpoints and SQL Server 2012+
- **MongoDB**: Atlas, Azure Cosmos DB for MongoDB and self-hosted servers, with mongosh-style queries and a Compass-like documents view

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

## MongoDB

Works with MongoDB 4.0 and newer, including Atlas and Azure Cosmos DB for MongoDB (server version 4.0+; for Cosmos, `retrywrites=false` is applied automatically).

Choose **MongoDB** at the top of the New Connection dialog and paste a connection string, e.g. `mongodb+srv://user@cluster0.example.mongodb.net/` (Atlas → Connect → Drivers) or `mongodb://localhost:27017`. A password inside the connection string is moved to the Keychain and removed from the saved string. If the user is defined in another database, add `?authSource=admin`.

The explorer lists databases → collections and views. Double-click a collection to open its documents; right-click for Count, Aggregate, Insert Document and Indexes.

Query tabs on a MongoDB connection accept **mongosh syntax**, one or more statements per tab:

```js
db.orders.find({ status: "paid", total: { $gt: Decimal128("100") } }, { item: 1, total: 1 })
  .sort({ createdAt: -1 })
  .limit(20)

db.orders.aggregate([
  { $match: { createdAt: { $gte: ISODate("2024-01-01") } } },
  { $group: { _id: "$status", n: { $sum: 1 } } }
])

db.orders.updateMany({ status: "new" }, { $set: { status: "paid" } })
use reporting
show collections
```

| | |
|---|---|
| **Commands** | `find` (with `sort`, `limit`, `skip`, `projection`, `count`, `explain`), `findOne`, `aggregate`, `countDocuments`, `estimatedDocumentCount`, `distinct`, `insertOne`/`insertMany`, `updateOne`/`updateMany`, `replaceOne`, `deleteOne`/`deleteMany`, `getIndexes`, `createIndex`, `dropIndex`, `stats`, `drop`; `db.runCommand`, `db.adminCommand`, `db.stats`, `db.getCollectionNames`, `use`, `show dbs`, `show collections` |
| **Values** | `ObjectId()`, `ISODate()`/`new Date()`, `Long()`/`NumberLong()`, `NumberInt()`, `Decimal128()`, `UUID()`, `BinData()`, `Timestamp()`, `/regex/i`, comments, unquoted keys, trailing commas |
| **Results** | **Documents** view (syntax-highlighted, collapsible objects and arrays — ⌥-click collapses a node and everything inside, Expand all / Collapse all — and Edit / Copy / Delete per document) and **Table** view (top-level fields as columns, copy and CSV export). JSON export writes Extended JSON |
| **Editing** | Edit opens the document in an editor; saving replaces it by `_id` and keeps every BSON type intact (int vs. Long vs. Double, dates, decimals). Results from a projection or an aggregation are read-only, so a partial document can never overwrite a full one |
| **Safety** | `find()` without `.limit()` shows the first 1,000 documents (page with `.skip()`); a script stops at the first error so later writes don't run |
| **Autocomplete** | Collection names after `db.`, methods, `$` operators, and field names sampled from the collection |

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

Tests (the integration tests need SQL Server and MongoDB, e.g. in Docker):

```sh
docker run -d -e ACCEPT_EULA=Y -e 'MSSQL_SA_PASSWORD=Str0ng!Passw0rd' -p 1433:1433 mcr.microsoft.com/mssql/server:2022-latest
docker run -d -e MONGO_INITDB_ROOT_USERNAME=admin -e 'MONGO_INITDB_ROOT_PASSWORD=Str0ng!Pass' -p 27017:27017 mongo:7
make test-integration
```

## Architecture

```
main.go / app.go          Wails app, native menu, bindings exposed to the UI
internal/engine           go-mssqldb: auth (azidentity), pools, per-tab sessions,
                          GO batch splitting, streaming execution, metadata queries
internal/mongo            MongoDB: connection strings, mongosh-syntax parser and printer
                          (exact BSON round-trip), command execution, document edits
internal/store            connection profiles (JSON) + Keychain secrets, settings, history
frontend/                 TypeScript + Vite, CodeMirror 6 editor, custom virtualized grid
```

Results stream from Go to the UI in chunks as events, so large queries render progressively and can be cancelled at any time. Settings live in `~/Library/Application Support/SQL Studio/`.

## License

MIT
