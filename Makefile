.PHONY: dev build build-universal test test-integration frontend bindings

# Run the app with hot reload (also serves a browser UI at http://localhost:34115)
dev:
	wails dev

# Build SQL Studio.app for this Mac's architecture -> build/bin/
build:
	wails build -clean -trimpath

# Build a universal (Apple Silicon + Intel) app
build-universal:
	wails build -platform darwin/universal -clean -trimpath
	codesign --force --deep --sign - "build/bin/SQL Studio.app"

frontend:
	cd frontend && npm install && npm run build

bindings:
	wails generate module

test:
	go test ./internal/...

# Requires SQL Server and MongoDB, e.g.:
#   docker run -d -e ACCEPT_EULA=Y -e 'MSSQL_SA_PASSWORD=Str0ng!Passw0rd' -p 1433:1433 mcr.microsoft.com/mssql/server:2022-latest
#   docker run -d -e MONGO_INITDB_ROOT_USERNAME=admin -e 'MONGO_INITDB_ROOT_PASSWORD=Str0ng!Pass' -p 27017:27017 mongo:7
test-integration:
	SQLSTUDIO_TEST_SERVER=localhost SQLSTUDIO_TEST_USER=sa SQLSTUDIO_TEST_PASSWORD='Str0ng!Passw0rd' \
	SQLSTUDIO_TEST_MONGO_URI='mongodb://admin:Str0ng%21Pass@localhost:27017/?authSource=admin' \
	go test -v ./internal/...
