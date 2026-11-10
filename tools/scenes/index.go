// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"html/template"
	"os"

	"github.com/jwriteclub/spec/internal/tui/scenes"
)

// indexPage is the gallery: one section per scene with its description,
// its steps and every capture, grouped by size, dark beside light.
var indexPage = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>spec ui scenes</title>
<style>
:root { --bg: #f6f6f4; --fg: #1d1d1f; --dim: #6b6b70; --card: #ffffff; --line: #e2e2de; }
@media (prefers-color-scheme: dark) { :root { --bg: #141416; --fg: #e8e8ea; --dim: #9a9aa0; --card: #1d1d20; --line: #2c2c30; } }
body { margin: 0; padding: 24px 16px 64px; background: var(--bg); color: var(--fg); font: 15px/1.5 -apple-system, system-ui, sans-serif; }
main { max-width: 1500px; margin: 0 auto; }
h1 { font-size: 24px; margin: 0 0 4px; }
.lead { color: var(--dim); margin: 0 0 24px; }
nav { display: flex; flex-wrap: wrap; gap: 6px 14px; margin-bottom: 32px; }
nav a { color: var(--fg); }
section { background: var(--card); border: 1px solid var(--line); border-radius: 10px; padding: 18px 20px; margin-bottom: 28px; }
h2 { font-size: 19px; margin: 0 0 4px; font-family: ui-monospace, Menlo, monospace; }
.desc { margin: 0 0 10px; }
ol { color: var(--dim); margin: 0 0 14px; padding-left: 22px; }
h3 { font-size: 14px; font-weight: 600; color: var(--dim); margin: 16px 0 8px; }
.pair { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
@media (max-width: 900px) { .pair { grid-template-columns: 1fr; } }
figure { margin: 0; }
img { width: 100%; height: auto; border-radius: 6px; border: 1px solid var(--line); display: block; }
figcaption { font-size: 12px; color: var(--dim); margin-top: 4px; font-family: ui-monospace, Menlo, monospace; word-break: break-all; }
</style>
</head>
<body>
<main>
<h1>spec ui scenes</h1>
<p class="lead">{{len .}} scenes of <code>spec ui</code> rendered from canned model replies and a frozen clock
(<code>go run ./tools/scenes</code>). Each PNG is at 2x; dark and light terminal profiles side by side.</p>
<nav>{{range .}}<a href="#{{.Name}}">{{.Name}}</a>{{end}}</nav>
{{range .}}
<section id="{{.Name}}">
<h2>{{.Name}}</h2>
<p class="desc">{{.Description}}</p>
{{if .Steps}}<ol>{{range .Steps}}<li>{{.}}</li>{{end}}</ol>{{end}}
{{range .Groups}}
<h3>{{.Title}}</h3>
<div class="pair">{{range .Files}}<figure><a href="{{.}}"><img src="{{.}}" alt="{{.}}" loading="lazy"></a><figcaption>{{.}}</figcaption></figure>{{end}}</div>
{{end}}
</section>
{{end}}
</main>
</body>
</html>
`))

// indexScene is one scene's section of the gallery.
type indexScene struct {
	Name, Description string
	Steps             []string
	Groups            []indexGroup
}

// indexGroup is one capture at one size, in every theme.
type indexGroup struct {
	Title string
	Files []string
}

// writeIndex writes the gallery of all to path.
func writeIndex(path string, all []scenes.Scene) error {
	var page []indexScene
	for _, sc := range all {
		is := indexScene{Name: sc.Name, Description: sc.Description}
		for _, st := range sc.Steps {
			is.Steps = append(is.Steps, st.StepText())
		}
		vs := sc.Variants()
		for _, capture := range sc.Captures() {
			var sizes []scenes.Size
			for _, v := range vs {
				if len(sizes) == 0 || sizes[len(sizes)-1] != v.Size {
					sizes = append(sizes, v.Size)
				}
			}
			for _, sz := range sizes {
				title := "final · " + sz.String()
				if capture != "" {
					title = "step " + capture + " · " + sz.String()
				}
				g := indexGroup{Title: title}
				for _, v := range vs {
					if v.Size == sz {
						g.Files = append(g.Files, sc.FileName(capture, v))
					}
				}
				is.Groups = append(is.Groups, g)
			}
		}
		page = append(page, is)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := indexPage.Execute(f, page); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
