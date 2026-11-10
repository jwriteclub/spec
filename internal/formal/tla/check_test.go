// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tla

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/jwriteclub/prudence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/formal"
)

// A clean model that extends a standard module and a project module is
// checked completely, semantics and levels, with no findings. The resolver is
// asked for every name, standard ones included (the workspace comes first).
func TestCheckerClean(t *testing.T) {
	asked := []string{}
	rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{
		Path: "tla/M.tla", Src: []byte(wrap("EXTENDS Naturals, Lib\nA == 1 + B")),
		Resolve: func(m string) (string, []byte, bool) {
			asked = append(asked, m)
			if m == "Lib" {
				return "tla/Lib.tla", []byte(wrap2("Lib", "B == 2")), true
			}
			return "", nil, false
		},
	})
	require.NoError(t, err)
	assert.Equal(t, CheckerName, rep.Checker)
	assert.NotEmpty(t, rep.Version)
	assert.Equal(t, formal.LevelComplete, rep.Level)
	assert.Equal(t, formal.CompletionComplete, rep.Completion)
	assert.True(t, formal.FullCheck(rep.Level, rep.Completion))
	assert.Empty(t, rep.Diagnostics)
	assert.Empty(t, rep.Incomplete)
	assert.Equal(t, []string{"Naturals", "Lib"}, asked)
}

// An undefined name across EXTENDS is a semantic error at its place.
func TestCheckerSemanticError(t *testing.T) {
	rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{
		Path: "tla/M.tla", Src: []byte(wrap("EXTENDS Naturals\nA == 1 + y")),
	})
	require.NoError(t, err)
	assert.Equal(t, formal.CompletionComplete, rep.Completion)
	require.Equal(t, 1, rep.Errors())
	d := rep.Diagnostics[0]
	assert.Equal(t, "semantic/4200", d.Code)
	assert.Equal(t, "semantic", d.Phase)
	assert.Equal(t, formal.Position{Line: 3, Column: 10, Offset: 45}, d.Start)
	assert.Equal(t, formal.Position{Line: 3, Column: 11, Offset: 46}, d.End)
}

// A module neither the project nor prudence has is SANY's error on its name.
func TestCheckerMissingModule(t *testing.T) {
	rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{
		Path: "tla/M.tla", Src: []byte(wrap("EXTENDS Naturals, Other\nA == 1 + 2")),
		Resolve: func(string) (string, []byte, bool) { return "", nil, false },
	})
	require.NoError(t, err)
	assert.Equal(t, formal.CompletionComplete, rep.Completion)
	require.Equal(t, 1, rep.Errors())
	d := rep.Diagnostics[0]
	assert.Equal(t, "semantic/4220", d.Code)
	assert.Equal(t, formal.Position{Line: 2, Column: 19, Offset: 37}, d.Start)
	assert.Contains(t, d.Message, "Cannot find source file for module Other")
}

// A level error is an error from the level phase ("complete"), on the
// construct SANY names.
func TestCheckerLevelError(t *testing.T) {
	src := wrap("VARIABLE x\nCONSTANT C\nASSUME C = x'")
	rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{Path: "tla/M.tla", Src: []byte(src)})
	require.NoError(t, err)
	assert.Equal(t, formal.LevelComplete, rep.Level)
	assert.Equal(t, formal.CompletionComplete, rep.Completion)
	require.Equal(t, 1, rep.Errors())
	d := rep.Diagnostics[0]
	assert.Equal(t, "semantic/4206", d.Code)
	assert.Equal(t, formal.LevelComplete, d.Phase)
	assert.Equal(t, 4, d.Start.Line)
	assert.Equal(t, "ASSUME C = x'", src[d.Start.Offset:d.End.Offset])
	assert.Contains(t, d.Message, "Level error")
	assert.NotContains(t, d.Message, "\n", "SANY's message is on one line")
}

// A definitive syntax rejection completes the check at level syntax, with no
// incomplete hint.
func TestCheckerSyntaxErrorCompletes(t *testing.T) {
	rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{Path: "tla/M.tla", Src: []byte(wrap("A == 1 + `"))})
	require.NoError(t, err)
	assert.Equal(t, "syntax", rep.Level)
	assert.Equal(t, formal.CompletionComplete, rep.Completion)
	assert.False(t, formal.FullCheck(rep.Level, rep.Completion), "complete at syntax is not a semantic check")
	assert.Equal(t, 1, rep.Errors())
	for _, d := range rep.Diagnostics {
		assert.NotEqual(t, prudence.CodeIncomplete, d.Code)
	}
}

// The resolver adapter: a module the project lacks is prudence's not-found
// sentinel, a found one is a workspace source, and an ended context is
// operational.
func TestResolverWrapsModuleNotFound(t *testing.T) {
	ctx := context.Background()
	ms := &modules{texts: map[string][]byte{}}

	_, err := ms.resolver(nil)(ctx, "Other")
	require.Error(t, err)
	assert.True(t, errors.Is(err, prudence.ErrModuleNotFound))
	assert.False(t, errors.Is(err, fs.ErrNotExist))
	assert.Contains(t, err.Error(), "Other")

	project := func(m string) (string, []byte, bool) {
		if m == "Lib" {
			return "tla/Lib.tla", []byte("---- MODULE Lib ----\n====\n"), true
		}
		return "", nil, false
	}
	_, err = ms.resolver(project)(ctx, "Other")
	assert.ErrorIs(t, err, prudence.ErrModuleNotFound)

	src, err := ms.resolver(project)(ctx, "Lib")
	require.NoError(t, err)
	assert.Equal(t, "tla/Lib.tla", src.Path)
	assert.Equal(t, prudence.OriginWorkspace, src.Origin)
	assert.Equal(t, src.Text, ms.text("tla/Lib.tla"), "kept for the diagnostics' positions")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = ms.resolver(project)(cancelled, "Lib")
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, errors.Is(err, prudence.ErrModuleNotFound), "an ended check is not a missing module")
}

func TestCheckSeverity(t *testing.T) {
	assert.Equal(t, formal.SeverityError, checkSeverity(prudence.SeverityError))
	assert.Equal(t, formal.SeverityWarning, checkSeverity(prudence.SeverityWarning))
	assert.Equal(t, formal.SeverityHint, checkSeverity(prudence.SeverityInfo))
	assert.Equal(t, formal.SeverityHint, checkSeverity(prudence.SeverityHint))
}

func TestCompletion(t *testing.T) {
	assert.Equal(t, formal.CompletionUnknown, completion(prudence.CompletionUnknown))
	assert.Equal(t, formal.CompletionComplete, completion(prudence.CompletionComplete))
	assert.Equal(t, formal.CompletionIncomplete, completion(prudence.CompletionIncomplete))
}

// A diagnostic's position is 1-based, its column in bytes, after multi-byte
// text on the line.
func TestCheckerPositions(t *testing.T) {
	src := wrap("A == \"é\" + `")
	rep, err := Checker{}.Check(context.Background(), formal.CheckRequest{Path: "tla/M.tla", Src: []byte(src)})
	require.NoError(t, err)
	require.NotEmpty(t, rep.Diagnostics)
	d := rep.Diagnostics[0]
	assert.Equal(t, "tla/M.tla", d.Path)
	assert.Equal(t, formal.SeverityError, d.Severity)
	assert.Equal(t, 2, d.Start.Line)
	assert.Equal(t, len("A == \"é\" + ")+1, d.Start.Column)
	assert.Equal(t, "`", src[d.Start.Offset:d.Start.Offset+1])
	assert.Equal(t, 1, rep.Errors())
}

func TestCheckerStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Checker{}.Check(ctx, formal.CheckRequest{Path: "tla/M.tla", Src: []byte(wrap("A == 1"))})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPosition(t *testing.T) {
	src := []byte("ab\nçd\n")
	assert.Equal(t, formal.Position{Line: 1, Column: 1, Offset: 0}, position(src, 0))
	assert.Equal(t, formal.Position{Line: 2, Column: 3, Offset: 5}, position(src, 5))
	assert.Equal(t, formal.Position{Line: 3, Column: 1, Offset: 7}, position(src, 99))
}

// wrap2 is wrap for a module named name.
func wrap2(name, body string) string {
	return "---- MODULE " + name + " ----\n" + body + "\n====\n"
}
