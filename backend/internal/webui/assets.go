package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
)

// Assets maps a Vite entry name to the <link>/<script> tags of its hashed build
// files, read once from the build manifest.
type Assets map[string]template.HTML

type manifestChunk struct {
	File    string   `json:"file"`
	Name    string   `json:"name"`
	IsEntry bool     `json:"isEntry"`
	CSS     []string `json:"css"`
	Imports []string `json:"imports"`
}

// LoadAssets reads <dir>/.vite/manifest.json written by `vite build` with build.manifest enabled.
func LoadAssets(dir string) (Assets, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".vite", "manifest.json"))
	if err != nil {
		return nil, errors.New("frontend build manifest is unavailable; build web and set WEB_DIST_DIR")
	}
	var chunks map[string]manifestChunk
	if err := json.Unmarshal(raw, &chunks); err != nil {
		return nil, errors.New("frontend build manifest is not valid JSON")
	}
	assets := Assets{}
	for _, entry := range chunks {
		if !entry.IsEntry || entry.Name == "" {
			continue
		}
		var tags strings.Builder
		seen := map[string]bool{}
		// Stylesheets of shared chunks come first so page styles can override them.
		var addCSS func(chunk manifestChunk)
		addCSS = func(chunk manifestChunk) {
			for _, key := range chunk.Imports {
				if !seen[key] {
					seen[key] = true
					addCSS(chunks[key])
				}
			}
			for _, file := range chunk.CSS {
				if !seen[file] {
					seen[file] = true
					fmt.Fprintf(&tags, `<link rel="stylesheet" href="/%s">`, template.HTMLEscapeString(file))
				}
			}
		}
		addCSS(entry)
		fmt.Fprintf(&tags, `<script type="module" src="/%s"></script>`, template.HTMLEscapeString(entry.File))
		assets[entry.Name] = template.HTML(tags.String())
	}
	return assets, nil
}

// Tags returns the tags for one entry; an empty name means the page has no script.
func (a Assets) Tags(entry string) (template.HTML, error) {
	if entry == "" {
		return "", nil
	}
	tags, ok := a[entry]
	if !ok {
		return "", fmt.Errorf("frontend entry %q is missing from the build", entry)
	}
	return tags, nil
}
