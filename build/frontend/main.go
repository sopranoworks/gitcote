package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"
	"github.com/sopranoworks/npmgo"
	"github.com/sopranoworks/npmgo/esbuildplugin"
)

func main() {
	if err := os.Chdir("../.."); err != nil {
		fmt.Fprintf(os.Stderr, "chdir to repo root: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("build/frontend: caching npm packages...")
	index, err := npmgo.CachePackages(npmgo.Options{
		LockfilePath: "web/package-lock.json",
		CacheOnly:    true,
		Logf:         func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "npmgo: %v\n", err)
		os.Exit(1)
	}

	outDir := "cmd/gitcote/dist"
	if err := os.RemoveAll(outDir); err != nil {
		fmt.Fprintf(os.Stderr, "clean %s: %v\n", outDir, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Join(outDir, "assets"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}

	fmt.Println("build/frontend: bundling with esbuild...")
	result := esbuild.Build(esbuild.BuildOptions{
		EntryPoints: []string{"web/src/main.tsx"},
		Bundle:      true,
		Outdir:      outDir,
		Splitting:   true,
		Format:      esbuild.FormatESModule,
		MinifyWhitespace:  true,
		MinifySyntax:      true,
		MinifyIdentifiers: true,
		Metafile: true,
		Write:    true,
		Plugins: []esbuild.Plugin{
			compatPlugin(),
			esbuildplugin.New(index),
		},
		Loader: map[string]esbuild.Loader{
			".module.css": esbuild.LoaderLocalCSS,
			".css":        esbuild.LoaderCSS,
			".svg":        esbuild.LoaderFile,
			".png":        esbuild.LoaderFile,
		},
		JSX:             esbuild.JSXAutomatic,
		JSXImportSource: "react",
		EntryNames:      "assets/[name]-[hash]",
		ChunkNames:      "assets/[name]-[hash]",
		AssetNames:      "assets/[name]-[hash]",
		Define: map[string]string{
			"process.env.NODE_ENV": `"production"`,
		},
	})

	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			loc := ""
			if e.Location != nil {
				loc = fmt.Sprintf("%s:%d:%d: ", e.Location.File, e.Location.Line, e.Location.Column)
			}
			fmt.Fprintf(os.Stderr, "esbuild: %s%s\n", loc, e.Text)
		}
		os.Exit(1)
	}

	if err := writeIndexHTML(result.Metafile, outDir); err != nil {
		fmt.Fprintf(os.Stderr, "index.html: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("build/frontend: done")
}

// compatPlugin resolves Node.js subpath imports (#-prefixed).
func compatPlugin() esbuild.Plugin {
	subpathImports := map[string]string{
		"#minpath": "vfile/lib/minpath.browser.js",
		"#minproc": "vfile/lib/minproc.browser.js",
		"#minurl":  "vfile/lib/minurl.browser.js",
	}

	return esbuild.Plugin{
		Name: "compat",
		Setup: func(build esbuild.PluginBuild) {
			build.OnResolve(esbuild.OnResolveOptions{Filter: `^#`}, func(args esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
				if target, ok := subpathImports[args.Path]; ok {
					return esbuild.OnResolveResult{Path: target, Namespace: "npmgo"}, nil
				}
				return esbuild.OnResolveResult{}, nil
			})
		},
	}
}

type metafile struct {
	Outputs map[string]metaOutput `json:"outputs"`
}

type metaOutput struct {
	EntryPoint string `json:"entryPoint,omitempty"`
	CSSBundle  string `json:"cssBundle,omitempty"`
}

func writeIndexHTML(metaJSON, outDir string) error {
	var meta metafile
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return fmt.Errorf("parse metafile: %w", err)
	}

	var jsPath, cssPath string
	for outPath, out := range meta.Outputs {
		if out.EntryPoint == "web/src/main.tsx" {
			jsPath = "/" + strings.TrimPrefix(outPath, outDir+"/")
			if out.CSSBundle != "" {
				cssPath = "/" + strings.TrimPrefix(out.CSSBundle, outDir+"/")
			}
			break
		}
	}
	if jsPath == "" {
		return fmt.Errorf("entry point web/src/main.tsx not found in metafile outputs")
	}

	var cssTag string
	if cssPath != "" {
		cssTag = fmt.Sprintf("\n    <link rel=\"stylesheet\" crossorigin href=\"%s\">", cssPath)
	}

	html := fmt.Sprintf(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover" />
    <meta name="color-scheme" content="dark light" />
    <title>GitCote</title>
    <script type="module" crossorigin src="%s"></script>%s
  </head>
  <body>
    <div id="root"></div>
  </body>
</html>
`, jsPath, cssTag)

	return os.WriteFile(filepath.Join(outDir, "index.html"), []byte(html), 0o644)
}
