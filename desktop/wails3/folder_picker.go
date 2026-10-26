package main

import (
	"encoding/json/v2"
	"log/slog"
	"net/url"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The web app asks for a native folder panel with this message and gets the answer as the
// dex:folder-picked window event. The browser has no such panel that returns a host path.
const pickFolderPrefix = "dex:pick-folder "

type folderRequest struct {
	ID    string `json:"id"`
	Start string `json:"start"`
}

// parseFolderRequest accepts a request only from the main frame of the Dex backend, so another
// page that the window might show cannot open the panel.
func parseFolderRequest(message string, origin *application.OriginInfo, backendURL string) (folderRequest, bool) {
	body, ok := strings.CutPrefix(message, pickFolderPrefix)
	if !ok || origin == nil || !origin.IsMainFrame || !sameOrigin(origin.Origin, backendURL) {
		return folderRequest{}, false
	}
	var request folderRequest
	if err := json.Unmarshal([]byte(body), &request); err != nil || request.ID == "" {
		return folderRequest{}, false
	}
	return request, true
}

func sameOrigin(a, b string) bool {
	x, err := url.Parse(a)
	if err != nil {
		return false
	}
	y, err := url.Parse(b)
	return err == nil && x.Scheme == y.Scheme && x.Host == y.Host
}

// handleFolderMessage shows the panel off the message loop, so other window messages keep moving
// while the user chooses. A cancelled panel answers with an empty path.
func handleFolderMessage(backendURL string) func(application.Window, string, *application.OriginInfo) {
	return func(window application.Window, message string, origin *application.OriginInfo) {
		request, ok := parseFolderRequest(message, origin, backendURL)
		if !ok {
			return
		}
		go func() {
			dialog := application.Get().Dialog.OpenFile().
				CanChooseDirectories(true).
				CanChooseFiles(false).
				CanCreateDirectories(true).
				SetTitle("Choose a project folder").
				SetButtonText("Choose").
				AttachToWindow(window)
			if request.Start != "" {
				dialog.SetDirectory(request.Start)
			}
			path, err := dialog.PromptForSingleSelection()
			if err != nil {
				slog.Warn("dex folder panel", "err", err)
			}
			detail, err := json.Marshal(map[string]string{"id": request.ID, "path": path})
			if err != nil {
				return
			}
			window.ExecJS("window.dispatchEvent(new CustomEvent('dex:folder-picked', {detail: " + string(detail) + "}))")
		}()
	}
}
