//go:build production || dev || bindings

package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var wailsAssets embed.FS

func main() {
	app := NewApp()

	sub, err := fs.Sub(wailsAssets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	if err := verifyServerFrontend(sub); err != nil {
		log.Fatal(err)
	}

	if err := wails.Run(&options.App{
		Title:            "MemoDump",
		Width:            1280,
		Height:           800,
		MinWidth:         800,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 248, G: 250, B: 253, A: 255},
		AssetServer: &assetserver.Options{
			Assets:  sub,
			Handler: buildAPIMux(),
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	}); err != nil {
		log.Fatal(err)
	}
}
