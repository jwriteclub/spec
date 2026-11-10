// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every displayed helper is reachable in its visible context. Compact width
// templates preserve essential commands, and every keycap/label fits the frame.
func TestHelpBarMatchesKeymap(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	for _, ctx := range Contexts {
		for _, w := range []int{80, 100, 140, 220} {
			t.Run(ctx.String()+fmt.Sprint(w), func(t *testing.T) {
				lines := a.helpBar(ctx, w)
				require.Len(t, lines, 2)
				for _, line := range lines {
					assert.Equal(t, w, ansi.StringWidth(line))
				}
				bs := helperTemplate(ctx, w)
				for _, b := range bs {
					found, ok := Lookup(ctx, b.Keys[0])
					require.True(t, ok)
					assert.Equal(t, b.Action, found.Action)
					text := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, " "))), " ")
					assert.Contains(t, text, b.Label)
				}
			})
		}
	}
}
func TestHelpBarExact(t *testing.T) {
	a := newTestApp(t, testOptions(lensdProject(t)))
	bs := helperTemplate(CtxEditor, 80)
	var actions []Action
	for _, b := range bs {
		actions = append(actions, b.Action)
	}
	assert.Equal(t, []Action{ActHelp, ActExit, ActWriteOut, ActQuickOpen, ActWhereIs, ActPropose, ActUndo, ActReview, ActOutline, ActNextPane}, actions)
	// A hidden state change cannot move or rename a helper.
	before := helperTemplate(CtxEditor, 140)
	send(a, typeText("x")...)
	assert.Equal(t, before, helperTemplate(CtxEditor, 140))
	for _, ascii := range []bool{false, true} {
		a.theme.ASCII = ascii
		lines := a.helpBar(CtxChecks, 140)
		assert.Contains(t, ansi.Strip(lines[0]), "[e]")
		assert.Contains(t, ansi.Strip(lines[1]), "[c]")
		for _, key := range []string{"^R", "Ret", "A", "Esc", "y", "e", "⇧Tab"} {
			assert.GreaterOrEqual(t, keycapWidth(displayKey(key, ascii)), 3)
		}
	}
}

// TestAltOnlyAliases: alt keys are aliases. Only the help bar toggle and
// jump forward (not common-flow keys) have no ctrl, function or plain key.
func TestAltOnlyAliases(t *testing.T) {
	for _, b := range Keymap {
		nonAlt := slices.ContainsFunc(b.Keys, func(k string) bool { return !strings.HasPrefix(k, "alt+") })
		if b.Action == ActHelpBar || b.Action == ActForward {
			continue // jump forward kept only its alt keys when ^F became Format (step 4d)
		}
		assert.True(t, nonAlt, "%s (%s) is bound only to alt keys", b.Key, b.Desc)
		assert.False(t, strings.HasPrefix(b.Keys[0], "alt+"), "%s: the primary key %s is an alt key", b.Desc, b.Keys[0])
	}
}

func TestNoForbiddenKeys(t *testing.T) {
	require.Equal(t, []string{"ctrl+s", "ctrl+j", "ctrl+h", "ctrl+i", "ctrl+m"}, ForbiddenKeys)
	for _, b := range Keymap {
		for _, k := range b.Keys {
			assert.NotContains(t, ForbiddenKeys, k, "%s binds a forbidden key", b.Desc)
		}
	}
	for _, ctx := range Contexts {
		for _, k := range ForbiddenKeys {
			_, ok := Lookup(ctx, k)
			assert.False(t, ok, "%s is bound in %s", k, ctx)
		}
	}
	// A terminal that sends BS (ctrl+h) for backspace still erases.
	b, ok := lookupKey(CtxEditor, key("ctrl+h"))
	require.True(t, ok)
	assert.Equal(t, ActBackspace, b.Action)
}

// TestStep4bKeys: ^P, ^R and ^N are live in the editor and the outline,
// and shown in the editor's help bar; Look, Refs and Outline moved to the
// help screen only.
func TestStep4bKeys(t *testing.T) {
	for k, act := range map[string]Action{"ctrl+p": ActPropose, "ctrl+r": ActReview, "ctrl+n": ActAnalyze} {
		for _, ctx := range []Context{CtxEditor, CtxOutline} {
			b, ok := Lookup(ctx, k)
			require.True(t, ok, k)
			assert.Equal(t, act, b.Action, k)
			assert.True(t, b.Bar, "%s is in the help bar", k)
		}
	}
	for _, k := range []string{"ctrl+l", "ctrl+\\"} {
		b, ok := Lookup(CtxEditor, k)
		require.True(t, ok, k)
		assert.False(t, b.Bar, "%s is on the help screen only", k)
	}
	// The outline toggle is ctrl+6 (^^) on the help bar, F2 and M-O too.
	for _, k := range []string{"ctrl+^", "ctrl+6", "f2", "alt+o"} {
		b, ok := Lookup(CtxEditor, k)
		require.True(t, ok, k)
		assert.Equal(t, ActOutline, b.Action, k)
		assert.True(t, b.Bar, k)
	}
	// ^R shows and focuses the Checks sidebar, from the editor and the
	// outline, and hides it when it has focus; with nothing in it, it says
	// how to fill it.
	dir := demoProject(t)
	a := newTestApp(t, testOptions(dir))
	send(a, key("ctrl+r"))
	assert.Equal(t, CtxChecks, a.context())
	assert.Contains(t, plain(a), "Nothing to review.")
	send(a, key("ctrl+r"))
	assert.Equal(t, CtxEditor, a.context())
	assert.False(t, a.sideShown)
	fixedProposals(t, dir)
	send(a, key("ctrl+r"))
	assert.Equal(t, CtxChecks, a.context())
	assert.Contains(t, plain(a), "Review")
	send(a, key("esc"))
	assert.Equal(t, CtxEditor, a.context())
	assert.True(t, a.sideShown, "Esc keeps the sidebar")
	send(a, key("f2"), key("ctrl+r"))
	assert.Equal(t, CtxChecks, a.context())
	b, ok := Lookup(CtxEditor, "f3")
	require.True(t, ok)
	assert.Equal(t, ActReview, b.Action, "F3 stays an alias")

	// In a list, ^P and ^N still move (fuzzy-finder style).
	b, ok = Lookup(CtxList, "ctrl+p")
	require.True(t, ok)
	assert.Equal(t, ActUp, b.Action)
}

// TestKeymapUnambiguous checks that no key is bound twice in a context and
// that every binding is reachable and documented.
func TestKeymapUnambiguous(t *testing.T) {
	for _, ctx := range Contexts {
		seen := map[string]string{}
		for _, b := range BindingsIn(ctx) {
			require.NotEmpty(t, b.Keys)
			assert.NotEmpty(t, b.Desc)
			assert.NotEmpty(t, b.Key)
			for _, k := range b.Keys {
				if prev, dup := seen[k]; dup {
					t.Errorf("%s: %q is bound to both %q and %q", ctx, k, prev, b.Desc)
				}
				seen[k] = b.Desc
			}
		}
	}
	// The plan's keys are where the plan says.
	for k, act := range map[string]Action{
		"ctrl+o": ActWriteOut, "ctrl+x": ActExit, "ctrl+w": ActWhereIs, "ctrl+g": ActHelp, "f1": ActHelp,
		"ctrl+_": ActGoToLine, "ctrl+/": ActGoToLine, "alt+g": ActGoToLine,
		"ctrl+a": ActLineStart, "ctrl+e": ActLineEnd, "pgup": ActPageUp, "ctrl+v": ActPageDown, "pgdown": ActPageDown,
		"right": ActRight, "left": ActLeft, "up": ActUp, "down": ActDown,
		"ctrl+home": ActTop, "alt+\\": ActTop, "ctrl+end": ActBottom, "alt+/": ActBottom,
		"ctrl+k": ActCut, "ctrl+u": ActPaste, "ctrl+c": ActCopy, "alt+6": ActCopy,
		"ctrl+^": ActOutline, "ctrl+6": ActOutline, "ctrl+space": ActMark, "alt+a": ActMark, "ctrl+d": ActDeleteChar,
		"ctrl+z": ActUndo, "alt+u": ActUndo, "ctrl+y": ActRedo, "alt+e": ActRedo,
		"ctrl+t": ActToTarget, "f12": ActToTarget,
		"ctrl+b": ActBack, "alt+left": ActBack, "alt+,": ActBack,
		"ctrl+f": ActFormat, "alt+right": ActForward, "alt+.": ActForward,
		"ctrl+q": ActQuickOpen, "ctrl+l": ActLook,
		"ctrl+\\": ActRefs, "alt+r": ActRefs, "shift+f12": ActRefs,
		"f2": ActOutline, "alt+o": ActOutline, "alt+x": ActHelpBar,
		"ctrl+left": ActWordLeft, "ctrl+right": ActWordRight, "shift+left": ActSelLeft,
		"ctrl+p": ActPropose, "ctrl+r": ActReview, "f3": ActReview, "ctrl+n": ActAnalyze,
		"ctrl+]": ActNextEdit, "f8": ActNextEdit, "shift+f8": ActPrevEdit,
	} {
		b, ok := Lookup(CtxEditor, k)
		if assert.True(t, ok, k) {
			assert.Equal(t, act, b.Action, k)
		}
	}
	// Gone: find next (an empty ^W answer repeats the search).
	_, ok := Lookup(CtxEditor, "alt+w")
	assert.False(t, ok, "alt+w")
}

// TestKeyStrings checks that the key strings in the table are what Bubble
// Tea v2 reports for those keys.
func TestKeyStrings(t *testing.T) {
	for _, s := range []string{"ctrl+o", "ctrl+z", "ctrl+q", "ctrl+\\", "ctrl+^", "ctrl+_", "alt+,", "alt+.", "alt+\\", "alt+/", "alt+6", "shift+left", "ctrl+left", "ctrl+home", "f2", "f8", "shift+f8", "ctrl+]", "f12", "shift+f12", "alt+left", "enter", "esc", "backspace", "pgdown"} {
		assert.Equal(t, s, key(s).Keystroke(), s)
	}
}
