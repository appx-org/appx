package project

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// piHarnessFS contains the project-local Pi prompt, skills, extensions, and
// settings that Appx scaffolds into every project under .pi/.
//
//go:embed templates/pi
var piHarnessFS embed.FS

func scaffoldPiHarness(projectDir string, proj *Project, baseDomain string) error {
	replacements := map[string]string{
		"{{name}}":      proj.Name,
		"{{port}}":      fmt.Sprintf("%d", proj.AssignedPort),
		"{{subdomain}}": fmt.Sprintf("%s.%s", proj.Name, baseDomain),
	}

	return fs.WalkDir(piHarnessFS, "templates/pi", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("templates/pi", path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		target := filepath.Join(projectDir, ".pi", filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		content, err := piHarnessFS.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		for old, next := range replacements {
			text = strings.ReplaceAll(text, old, next)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		mode := fs.FileMode(0644)
		if strings.HasSuffix(target, ".py") {
			mode = 0755
		}
		if err := os.WriteFile(target, []byte(text), mode); err != nil {
			return err
		}
		return nil
	})
}
