// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/diag"
	"github.com/jwriteclub/spec/internal/formal"
	"github.com/jwriteclub/spec/internal/lint"
)

const (
	checkTop = "---- MODULE Top ----\nEXTENDS Naturals, Base\nUse == Helper + 1\n====\n"
	checkLib = "---- MODULE Base ----\nEXTENDS Naturals\nHelper == 2\n====\n"
)

// checkFiles scans path/text pairs as models.
func checkFiles(kv ...string) []*formal.File {
	var out []*formal.File
	for i := 0; i < len(kv); i += 2 {
		out = append(out, ScanModel(kv[i], []byte(kv[i+1])))
	}
	return out
}

// countingChecker counts checks of the real checker.
type countingChecker struct {
	inner formal.Checker
	n     map[string]int
}

func (c *countingChecker) Check(ctx context.Context, req formal.CheckRequest) (formal.Report, error) {
	c.n[req.Path]++
	return c.inner.Check(ctx, req)
}

// A name defined in an extended module resolves; removing it there is an
// error in the model that uses it, at the use, and only that model and the
// changed one are checked again.
func TestCheckModelsAcrossExtends(t *testing.T) {
	root := t.TempDir()
	cc := &countingChecker{inner: formal.CheckerFor("x.tla"), n: map[string]int{}}
	formal.RegisterChecker(".tla", cc)
	t.Cleanup(func() { formal.RegisterChecker(".tla", cc.inner) })
	cache := NewCheckCache()

	got := cache.CheckModels(context.Background(), root, checkFiles("tla/Top.tla", checkTop, "tla/Base.tla", checkLib), formal.CheckLimits{})
	require.Len(t, got, 2)
	for p, c := range got {
		assert.True(t, c.Ran, p)
		assert.NoError(t, c.Err)
		assert.Empty(t, c.Diags, p)
		assert.Equal(t, formal.LevelComplete, c.Report.Level, p)
		assert.Equal(t, "prudence: checked", c.Note())
	}

	broken := strings.Replace(checkLib, "Helper == 2", "Other == 2", 1)
	got = cache.CheckModels(context.Background(), root, checkFiles("tla/Top.tla", checkTop, "tla/Base.tla", broken), formal.CheckLimits{})
	assert.Empty(t, got["tla/Base.tla"].Diags)
	ds := got["tla/Top.tla"].Diags
	require.Len(t, ds, 1)
	d := ds[0]
	assert.Equal(t, diag.CodeFormalSemantic, d.Code)
	assert.Equal(t, wantSemantic(got["tla/Top.tla"]), d.Severity)
	assert.Equal(t, "tla/Top.tla", d.File)
	assert.Contains(t, d.Message, "Helper")
	assert.Equal(t, "Helper", checkTop[d.Range.Start:d.Range.End], "the range is the use, in bytes")
	assert.Equal(t, map[string]int{"tla/Top.tla": 2, "tla/Base.tla": 2}, cc.n)

	got = cache.CheckModels(context.Background(), root, checkFiles("tla/Top.tla", checkTop, "tla/Base.tla", broken), formal.CheckLimits{})
	assert.Len(t, got["tla/Top.tla"].Diags, 1, "from the cache")
	assert.Equal(t, map[string]int{"tla/Top.tla": 2, "tla/Base.tla": 2}, cc.n, "nothing changed, nothing checked")
}

// A file appearing on disk with a standard module's name invalidates the
// checks that used the embedded standard module.
func TestCheckModelsWorkspaceShadowsStandard(t *testing.T) {
	root := t.TempDir()
	top := "---- MODULE Top ----\nEXTENDS Naturals\nUse == 1 + 2\n====\n"
	cache := NewCheckCache()
	got := cache.CheckModels(context.Background(), root, checkFiles("Top.tla", top), formal.CheckLimits{})
	require.Empty(t, got["Top.tla"].Diags)

	require.NoError(t, os.WriteFile(filepath.Join(root, "Naturals.tla"), []byte("---- MODULE Naturals ----\nNat == 0\n====\n"), 0o644))
	got = cache.CheckModels(context.Background(), root, checkFiles("Top.tla", top), formal.CheckLimits{})
	ds := got["Top.tla"].Diags
	require.NotEmpty(t, ds, "the workspace's Naturals has no +")
	assert.Equal(t, diag.CodeFormalSemantic, ds[0].Code)
}

// A model with a syntax error is not checked: formal-syntax reports it.
func TestCheckModelsSkipsSyntaxErrors(t *testing.T) {
	got := NewCheckCache().CheckModels(context.Background(), t.TempDir(), checkFiles("M.tla", "---- MODULE M ----\nA == 1 + `\n====\n"), formal.CheckLimits{})
	assert.False(t, got["M.tla"].Ran)
	assert.Empty(t, got["M.tla"].Note())
}

// A module nobody has is an error on its name in the EXTENDS line, as SANY
// reports it.
func TestCheckModelsMissingModuleIsAnError(t *testing.T) {
	var nilCache *CheckCache
	src := "---- MODULE M ----\nEXTENDS Nowhere\nA == 1\n====\n"
	got := nilCache.CheckModels(context.Background(), t.TempDir(), checkFiles("M.tla", src), formal.CheckLimits{})
	c := got["M.tla"]
	assert.True(t, c.Ran)
	require.Len(t, c.Diags, 1)
	assert.Equal(t, diag.CodeFormalSemantic, c.Diags[0].Code)
	assert.Equal(t, diag.Error, c.Diags[0].Severity)
	assert.Equal(t, "Nowhere", src[c.Diags[0].Range.Start:c.Diags[0].Range.End])
	assert.Contains(t, c.Diags[0].Message, "Cannot find source file for module Nowhere")
	assert.Equal(t, formal.CompletionComplete, c.Report.Completion)
}

// A level error is a formal-semantic error too.
func TestCheckModelsLevelError(t *testing.T) {
	src := "---- MODULE M ----\nVARIABLE x\nCONSTANT C\nASSUME C = x'\n====\n"
	got := NewCheckCache().CheckModels(context.Background(), t.TempDir(), checkFiles("M.tla", src), formal.CheckLimits{})
	ds := got["M.tla"].Diags
	require.Len(t, ds, 1)
	assert.Equal(t, diag.CodeFormalSemantic, ds[0].Code)
	assert.Equal(t, diag.Error, ds[0].Severity)
	assert.Equal(t, "ASSUME C = x'", src[ds[0].Range.Start:ds[0].Range.End])
	assert.Contains(t, ds[0].Message, "Level error")
}

// spec lint sees the finding through LintOptions.
func TestLintOptionsCarryModelChecks(t *testing.T) {
	dir := tree(t, map[string]string{
		"spec.toml":    `formal = ["tla/*.tla"]`,
		"a.spec.md":    formalSpec,
		"tla/Top.tla":  checkTop,
		"tla/Base.tla": strings.Replace(checkLib, "Helper == 2", "Other == 2", 1),
	})
	l, err := Load(dir, nil)
	require.NoError(t, err)
	var sem []diag.Diagnostic
	for _, d := range lint.Run(l.Spec, l.LintOptions()) {
		if d.Code == diag.CodeFormalSemantic {
			sem = append(sem, d)
		}
	}
	require.Len(t, sem, 1)
	assert.Equal(t, "tla/Top.tla", sem[0].File)
	assert.Equal(t, 3, l.Formal.File("tla/Top.tla").Index().Position(sem[0].Range.Start).Line)
}

// wantSemantic is the severity of a semantic error from c: an error once
// the check completes, a hint until then.
func wantSemantic(c ModelCheck) diag.Severity {
	if c.Report.Completion == formal.CompletionComplete {
		return diag.Error
	}
	return diag.Hint
}

// failingChecker fails its first fail checks with an operational error,
// then defers to the real checker.
type failingChecker struct {
	inner formal.Checker
	fail  int
	n     int
}

func (c *failingChecker) Check(ctx context.Context, req formal.CheckRequest) (formal.Report, error) {
	c.n++
	if c.n <= c.fail {
		return formal.Report{}, context.DeadlineExceeded
	}
	return c.inner.Check(ctx, req)
}

// An operational failure, such as the check timing out, is not cached: the
// next reparse checks again and gets the real result.
func TestCheckModelsDoesNotCacheErrors(t *testing.T) {
	fc := &failingChecker{inner: formal.CheckerFor("x.tla"), fail: 1}
	formal.RegisterChecker(".tla", fc)
	t.Cleanup(func() { formal.RegisterChecker(".tla", fc.inner) })
	cache := NewCheckCache()
	files := checkFiles("Top.tla", "---- MODULE Top ----\nEXTENDS Naturals\nUse == 1 + 2\n====\n")

	got := cache.CheckModels(context.Background(), t.TempDir(), files, formal.CheckLimits{})
	require.True(t, errors.Is(got["Top.tla"].Err, context.DeadlineExceeded))

	got = cache.CheckModels(context.Background(), t.TempDir(), files, formal.CheckLimits{})
	assert.NoError(t, got["Top.tla"].Err, "checked again, not the cached failure")
	assert.Equal(t, "prudence: checked", got["Top.tla"].Note())
	assert.Equal(t, 2, fc.n)

	cache.CheckModels(context.Background(), t.TempDir(), files, formal.CheckLimits{})
	assert.Equal(t, 2, fc.n, "a finished check is cached")
}

// concurrentChecker resolves every module its model extends from several
// goroutines at once before deferring to the real checker.
type concurrentChecker struct {
	inner formal.Checker
}

func (c concurrentChecker) Check(ctx context.Context, req formal.CheckRequest) (formal.Report, error) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, name := range []string{"Base", "Naturals", "Missing"} {
				req.Resolve(name)
			}
		}()
	}
	wg.Wait()
	return c.inner.Check(ctx, req)
}

// The shared module memo survives a checker that resolves concurrently
// (run with -race to see a data race).
func TestCheckModelsConcurrentResolve(t *testing.T) {
	inner := formal.CheckerFor("x.tla")
	formal.RegisterChecker(".tla", concurrentChecker{inner: inner})
	t.Cleanup(func() { formal.RegisterChecker(".tla", inner) })
	got := NewCheckCache().CheckModels(context.Background(), t.TempDir(), checkFiles("tla/Top.tla", checkTop, "tla/Base.tla", checkLib), formal.CheckLimits{})
	for p, c := range got {
		assert.NoError(t, c.Err, p)
		assert.Empty(t, c.Diags, p)
	}
}
