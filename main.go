package main

import (
	"embed"
	"log"
	"os"
	goruntime "runtime"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/thennavanr/sql-studio/internal/store"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is set at build time with -ldflags "-X main.version=..."
var version = "0.1.0"

func main() {
	fixPath()

	dir, err := store.DefaultDir()
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(dir, store.Keychain{})
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp(st)

	err = wails.Run(&options.App{
		Title:            "SQL Studio",
		Width:            1360,
		Height:           860,
		MinWidth:         800,
		MinHeight:        500,
		BackgroundColour: &options.RGBA{R: 30, G: 30, B: 32, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets},
		Menu:             buildMenu(app),
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: false,
			About: &mac.AboutInfo{
				Title:   "SQL Studio " + version,
				Message: "A lightweight SQL Server & Azure SQL client.",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

func buildMenu(app *App) *menu.Menu {
	emit := func(action string) func(*menu.CallbackData) {
		return func(*menu.CallbackData) { runtime.EventsEmit(app.ctx, "menu", action) }
	}
	isMac := goruntime.GOOS == "darwin"
	m := menu.NewMenu()
	if isMac {
		m.Append(menu.AppMenu())
	}

	file := m.AddSubmenu("File")
	file.AddText("New Query", keys.CmdOrCtrl("n"), emit("newQuery"))
	file.AddText("New Connection…", keys.Combo("n", keys.CmdOrCtrlKey, keys.ShiftKey), emit("newConnection"))
	file.AddSeparator()
	file.AddText("Open…", keys.CmdOrCtrl("o"), emit("open"))
	file.AddText("Save", keys.CmdOrCtrl("s"), emit("save"))
	file.AddText("Save As…", keys.Combo("s", keys.CmdOrCtrlKey, keys.ShiftKey), emit("saveAs"))
	file.AddSeparator()
	file.AddText("Close Tab", keys.CmdOrCtrl("w"), emit("closeTab"))

	if isMac {
		m.Append(menu.EditMenu()) // required for ⌘C/⌘V/⌘A to work in the webview
	}

	query := m.AddSubmenu("Query")
	query.AddText("Execute", keys.CmdOrCtrl("e"), emit("execute"))
	query.AddText("Execute Current Statement", keys.Combo("e", keys.CmdOrCtrlKey, keys.ShiftKey), emit("executeCurrent"))
	query.AddText("Cancel Query", keys.CmdOrCtrl("."), emit("cancel"))
	query.AddText("Validate Query", keys.Combo("p", keys.CmdOrCtrlKey, keys.ShiftKey), emit("validate"))
	query.AddSeparator()
	query.AddText("Format Query", keys.Combo("f", keys.OptionOrAltKey, keys.ShiftKey), emit("format"))
	query.AddText("Toggle Comment", keys.CmdOrCtrl("/"), emit("toggleComment"))
	query.AddText("Comment Lines", nil, emit("comment"))
	query.AddText("Uncomment Lines", nil, emit("uncomment"))
	query.AddText("Toggle Results Pane", keys.CmdOrCtrl("r"), emit("toggleResults"))

	view := m.AddSubmenu("View")
	view.AddText("Toggle Sidebar", keys.CmdOrCtrl("b"), emit("toggleSidebar"))
	view.AddText("Query History", keys.CmdOrCtrl("y"), emit("history"))
	view.AddText("Settings…", keys.CmdOrCtrl(","), emit("settings"))
	view.AddSeparator()
	view.AddText("Zoom In", keys.CmdOrCtrl("="), emit("zoomIn"))
	view.AddText("Zoom Out", keys.CmdOrCtrl("-"), emit("zoomOut"))
	view.AddText("Reset Zoom", keys.CmdOrCtrl("0"), emit("zoomReset"))

	if isMac {
		m.Append(menu.WindowMenu())
	}
	return m
}

// fixPath makes Homebrew-installed tools (e.g. the Azure CLI used by
// "Azure CLI" authentication) visible: macOS apps launched from Finder get a
// minimal PATH that excludes /opt/homebrew/bin and /usr/local/bin.
func fixPath() {
	path := os.Getenv("PATH")
	for _, p := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if !strings.Contains(":"+path+":", ":"+p+":") {
			path = path + ":" + p
		}
	}
	_ = os.Setenv("PATH", path)
}
