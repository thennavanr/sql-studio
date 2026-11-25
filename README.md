# SQL Studio

A lightweight, native-feeling **SQL Server / Azure SQL**, **MongoDB** and **Redis** client for macOS — a fast replacement for Azure Data Studio and a light alternative to MongoDB Compass and RedisInsight.

- **~15 MB app**, starts in under a second (uses the macOS system WebKit, not Electron)
- First-class **Microsoft Entra ID** sign-in: interactive browser (MFA), `az login`, device code, service principal, default credential chain — plus classic SQL logins
- Passwords and client secrets are stored in the **macOS Keychain**, never on disk
- Works with Azure SQL Database, Azure SQL Managed Instance, Synapse, Fabric SQL endpoints and SQL Server 2012+
- **MongoDB**: Atlas, Azure Cosmos DB for MongoDB and self-hosted servers, with mongosh-style queries and a Compass-like documents view
- **Redis**: Azure Cache for Redis, Redis Cloud and self-hosted Redis / Valkey, with a key browser and redis-cli style commands
- The Connections panel has **SQL / MongoDB / Redis tabs**, each listing only its connections

## Features

| | |
|---|---|
| **Query editor** | Toolbar above the editor with Run, Cancel, connection, database, **Validate**, **Format**, **Comment** and **Uncomment**. T-SQL syntax highlighting, context-aware autocomplete (keywords and tables; in SELECT / WHERE / ON / GROUP BY / ORDER BY / SET it offers the columns of the tables the statement references — even when FROM comes after the cursor — resolves aliases, and qualifies column names shared by several tables), multiple tabs, find/replace, multi-cursor, open/save `.sql` files, tabs restored on relaunch |
| **Validate** | Checks the selection or the whole query **without running it** (⇧⌘P). SQL Server: compiles with `SET NOEXEC ON` (syntax, invalid columns) and builds estimated plans (missing tables, views, procedures), on a throwaway connection; temp tables and objects created earlier in the script are understood. MongoDB: syntax, supported methods and modifiers, and collections that do not exist. Errors are clickable and jump to the line |
| **Execution** | Runs selection, current statement (⇧⌘E) or whole script; `GO` / `GO n` batch separators; cancel (⌘.); optional timeout. Each tab keeps its own connection, so `#temp` tables, `SET` options and transactions persist like in SSMS/ADS |
| **Results** | Virtualized grid (100k+ rows), multiple result sets, sort, resize, rectangular selection with sum/avg, copy as TSV/CSV/JSON/Markdown/INSERT, export CSV/JSON, cell viewer with JSON/XML pretty-print |
| **Messages** | `PRINT` output, rows affected, SSMS-style errors (`Msg 208, Level 16, State 1, Line 3`) — click to jump to the line |
| **Object explorer** | Databases → tables, views, procedures, functions → columns (types, PK, identity). **Drag** a table, view, procedure or function into the editor to insert `[db].[schema].[name]` (columns insert `[column]`, databases `[db]`, MongoDB collections `db.collection`). Filter, Select Top 1000, Count Rows, Copy Name / Copy Full Name, Script as CREATE/ALTER/EXECUTE, **Script Data as INSERT** (TOP, WHERE filter, ORDER BY, target table, identity values, optional CREATE TABLE) to a new query, the clipboard or a file |
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

## Redis

Choose **Redis** in the New Connection dialog (or press **+** on the Redis tab). Enter `redis://host:6379`, `rediss://…` for TLS, or paste the Azure portal string (*Access keys → Primary connection string*, e.g. `myapp.redis.cache.windows.net:6380,password=…,ssl=True`). Azure Cache for Redis uses TLS on port 6380 with an access key as the password; a password in the string is moved to the Keychain.

The explorer lists databases with keys (db0, db3, …) → keys grouped by type (Strings, Hashes, Lists, Sets, Sorted Sets, Streams, Other; the first 2,000 keys). Double-click a key to show its value and TTL; right-click for TTL/type/memory, Copy Name and Delete Key.

Query tabs on a Redis connection take **redis-cli style commands**, one per line, with `"double"` / `'single'` quoted arguments and `#` comments:

```
HGETALL user:1
ZRANGE leaderboard 0 -1 WITHSCORES
SCAN 0 MATCH session:* COUNT 100
SELECT 3
GET only-in-db3
```

Replies appear in the grid (lists with a `#` column, hashes as field/value, sorted sets as member/score, streams as id/fields, SCAN with its next cursor). Validate checks command names and argument counts against the server (`COMMAND INFO`) without running anything. Destructive commands (`FLUSHALL`, `FLUSHDB`, `SHUTDOWN`, `CONFIG SET`, …) ask for confirmation, a script stops at the first error, and blocking commands (`SUBSCRIBE`, `MONITOR`, `MULTI`) are not supported. Redis Cluster mode is not supported yet.

## Keyboard shortcuts

| Shortcut | Action |
|---|---|
| ⌘E / ⌘↵ / F5 | Execute selection or whole script |
| ⇧⌘E / ⇧⌘↵ | Execute current statement (block around cursor) |
| ⌘. | Cancel |
| ⌘N / ⌘W | New / close query tab |
| ⇧⌘] / ⇧⌘[ | Next / previous tab |
| ⌘O / ⌘S / ⇧⌘S | Open / save / save as |
| ⇧⌘P | Validate (check without running) |
| ⌥⇧F | Format SQL or MongoDB query |
| ⌘/ | Toggle comment on the selected lines |
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

Tests (the integration tests need SQL Server, MongoDB and Redis, e.g. in Docker):

```sh
docker run -d -e ACCEPT_EULA=Y -e 'MSSQL_SA_PASSWORD=Str0ng!Passw0rd' -p 1433:1433 mcr.microsoft.com/mssql/server:2022-latest
docker run -d -e MONGO_INITDB_ROOT_USERNAME=admin -e 'MONGO_INITDB_ROOT_PASSWORD=Str0ng!Pass' -p 27017:27017 mongo:7
docker run -d -p 6379:6379 redis:7
make test-integration
```

## Architecture

```
main.go / app.go          Wails app, native menu, bindings exposed to the UI
internal/engine           go-mssqldb: auth (azidentity), pools, per-tab sessions,
                          GO batch splitting, streaming execution, metadata queries
internal/mongo            MongoDB: connection strings, mongosh-syntax parser and printer
                          (exact BSON round-trip), command execution, document edits
internal/redisdb          Redis: connection strings (incl. Azure format), key browser, redis-cli
                          command parsing and execution, validation
internal/store            connection profiles (JSON) + Keychain secrets, settings, history
frontend/                 TypeScript + Vite, CodeMirror 6 editor, custom virtualized grid
```

Results stream from Go to the UI in chunks as events, so large queries render progressively and can be cancelled at any time. Settings live in `~/Library/Application Support/SQL Studio/`.

## License

MIT
