// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Command scenes renders the `spec ui` scenes (internal/tui/scenes) to PNG
// for design review, with an index.html gallery:
//
//	go run ./tools/scenes [-out DIR] [-scene NAME]... [-j N] [-trace]
//
// Each capture is written as DIR/<scene>[-<step>]-<theme>-<w>x<h>.png. The
// model replies are canned and the clock frozen, so two runs write the
// same bytes.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jwriteclub/spec/internal/tui/scenes"
	"github.com/jwriteclub/spec/internal/tui/shot"
)

// sceneList is a repeatable -scene flag.
type sceneList []string

func (l *sceneList) String() string     { return strings.Join(*l, ",") }
func (l *sceneList) Set(s string) error { *l = append(*l, strings.Split(s, ",")...); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "scenes:", err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", ".dev/scenes", "directory to write the PNGs and index.html to")
	var names sceneList
	flag.Var(&names, "scene", "render only this scene (repeatable, or comma-separated)")
	jobs := flag.Int("j", min(4, max(runtime.NumCPU()/2, 1)), "scenes to run at once")
	trace := flag.Bool("trace", false, "print each step's plain frame (first variant only)")
	flag.Parse()

	all := scenes.All()
	if len(names) > 0 {
		var picked []scenes.Scene
		for _, n := range names {
			sc, ok := scenes.Find(n)
			if !ok {
				return fmt.Errorf("no scene %q", n)
			}
			picked = append(picked, sc)
		}
		all = picked
	}
	root, err := scenes.FindRepoRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	start := time.Now()

	type task struct {
		sc scenes.Scene
		v  scenes.Variant
	}
	var tasks []task
	for _, sc := range all {
		for _, v := range sc.Variants() {
			tasks = append(tasks, task{sc, v})
		}
	}
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(*jobs, 1))
	for k, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Explicit stable fixture paths keep config-source text deterministic.
			fixtureDir, err := filepath.Abs(filepath.Join(*out, ".fixtures", t.sc.Name+"-"+string(t.v.Theme)+"-"+t.v.Size.String()))
			if err != nil {
				errs[k] = err
				return
			}
			if err = os.MkdirAll(fixtureDir, 0700); err != nil {
				errs[k] = err
				return
			}
			defer os.RemoveAll(fixtureDir)
			o := scenes.Options{RepoRoot: root, TempDir: fixtureDir}
			if *trace && t.v == t.sc.Variants()[0] {
				o.Trace = func(step int, frame string) {
					fmt.Printf("---- %s step %d (%s)\n%s\n", t.sc.Name, step, t.v.Size, frame)
				}
			}
			shots, err := scenes.Run(t.sc, t.v, o)
			if err != nil {
				errs[k] = fmt.Errorf("%s %s %s: %w", t.sc.Name, t.v.Theme, t.v.Size, err)
				return
			}
			for _, s := range shots {
				path := filepath.Join(*out, t.sc.FileName(s.Capture, t.v))
				if err := shot.WriteFile(path, s.Frame); err != nil {
					errs[k] = err
					return
				}
			}
		}()
	}
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed++
		}
	}
	if err := writeIndex(filepath.Join(*out, "index.html"), all); err != nil {
		return err
	}
	fmt.Printf("%d scenes, %d captures in %s: %s\n", len(all), countCaptures(all), time.Since(start).Round(10*time.Millisecond), filepath.Join(*out, "index.html"))
	if failed > 0 {
		return fmt.Errorf("%d of %d runs failed", failed, len(tasks))
	}
	return nil
}

// countCaptures counts the PNGs the scenes write.
func countCaptures(all []scenes.Scene) int {
	n := 0
	for _, sc := range all {
		n += len(sc.Variants()) * len(sc.Captures())
	}
	return n
}
