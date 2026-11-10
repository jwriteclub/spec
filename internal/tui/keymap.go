// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Context is the input context: which bindings apply and what the help bar
// shows.
type Context int

// Contexts.
const (
	// CtxEditor is the editor with nothing open over it.
	CtxEditor Context = iota
	// CtxOutline is the outline pane with focus.
	CtxOutline
	// CtxPopup is a hover popup over the editor. Keys it does
	// not bind close it and go to the editor.
	CtxPopup
	// CtxList is a list popup: quick open or references.
	CtxList
	// CtxPrompt is a one-line prompt: where is, go to line.
	CtxPrompt
	// CtxConfirm is "Save modified buffer?" on exit.
	CtxConfirm
	// CtxConflict is the banner for a file changed on disk while modified.
	CtxConflict
	// CtxHelp is the help screen.
	CtxHelp
	// CtxCompletion is the completion popup while typing a $Name or §N.
	CtxCompletion
	// CtxChecks is the Checks sidebar with focus (^R).
	CtxChecks
	// CtxCommit is the commit view (A in the sidebar).
	CtxCommit
	// CtxReview is the review screen of one proposal.
	CtxReview
	// CtxHunkEdit is the small editor for a hunk's replacement text.
	CtxHunkEdit
	// CtxSaveFirst asks to save modified buffers before a check.
	CtxSaveFirst
	// CtxNotice is a modal message over any screen: any key closes it.
	CtxNotice
	// CtxDiagPopup is a popup that shows a diagnostic (hovered, or ^L):
	// ^P proposes its fix.
	CtxDiagPopup
	CtxSettings
)

// Contexts lists every context, in help-screen order.
var Contexts = []Context{CtxEditor, CtxOutline, CtxPopup, CtxDiagPopup, CtxList, CtxPrompt, CtxConfirm, CtxConflict, CtxHelp,
	CtxCompletion, CtxChecks, CtxCommit, CtxReview, CtxHunkEdit, CtxSaveFirst, CtxNotice, CtxSettings}

// String names the context.
func (c Context) String() string {
	switch c {
	case CtxEditor:
		return "Editor"
	case CtxOutline:
		return "Outline"
	case CtxPopup:
		return "Popup"
	case CtxDiagPopup:
		return "Diagnostic popup"
	case CtxList:
		return "List"
	case CtxPrompt:
		return "Prompt"
	case CtxConfirm:
		return "Save on exit"
	case CtxConflict:
		return "File changed on disk"
	case CtxHelp:
		return "Help"
	case CtxSettings:
		return "Settings"
	case CtxCompletion:
		return "Completion"
	case CtxChecks:
		return "Checks sidebar"
	case CtxCommit:
		return "Commit"
	case CtxReview:
		return "Review"
	case CtxHunkEdit:
		return "Edit text"
	case CtxSaveFirst:
		return "Save before a check"
	case CtxNotice:
		return "Notice"
	}
	return "?"
}

// Action is what a binding does.
type Action int

// Actions.
const (
	ActNone Action = iota
	ActHelp
	ActWriteOut
	ActExit
	ActWhereIs
	ActGoToLine
	ActLineStart
	ActLineEnd
	ActPageUp
	ActPageDown
	ActRight
	ActLeft
	ActUp
	ActDown
	ActTop
	ActBottom
	ActWordLeft
	ActWordRight
	ActSelLeft
	ActSelRight
	ActSelUp
	ActSelDown
	ActSelLineStart
	ActSelLineEnd
	ActCut
	ActPaste
	ActCopy
	ActMark
	ActDeleteChar
	ActBackspace
	ActNewline
	ActTab
	ActUndo
	ActRedo
	ActToTarget
	ActBack
	ActForward
	ActQuickOpen
	ActLook
	ActRefs
	ActOutline
	ActHelpBar
	ActCancel
	ActAccept
	ActYes
	ActNo
	ActKeepMine
	ActTakeTheirs
	ActShowDiff
	ActPropose
	ActReview
	ActAnalyze
	ActNextEdit
	ActPrevEdit
	ActAcceptHunk
	ActSkipHunk
	ActFeedback
	ActEditHunk
	ActApprove
	ActRejectAll
	ActDone
	ActFix
	ActFormat
	ActMessage
	ActCommitView
	ActToggle
	ActShowRemoved
	ActNextPane
	ActRetryCheck
	ActRefreshSelection
	ActSettings
	ActRevertSettings
	ActTestSettings
	ActSections
)

// Binding is one entry of the keymap: the keys (as Bubble Tea key strings),
// how the help bar shows them, and where they apply. Keys[0] is the
// primary key; the rest are aliases (alt keys are only ever aliases).
type Binding struct {
	Keys   []string
	Key    string // help bar form, nano style: "^O", "F2"
	Label  string // help bar label
	Desc   string // help screen description
	Action Action
	In     []Context
	// Bar is true when the help bar shows the binding.
	Bar bool
}

// ForbiddenKeys are never bound: ctrl+s is flow control on some terminals,
// and ctrl+j, ctrl+h, ctrl+i and ctrl+m are the control codes of newline,
// backspace, tab and enter. (A terminal that sends ctrl+h for backspace
// still gets a backspace: see keyAliases.)
var ForbiddenKeys = []string{"ctrl+s", "ctrl+j", "ctrl+h", "ctrl+i", "ctrl+m"}

// keyAliases maps a key string a terminal may send for a plain key to that
// key; lookupKey applies it before the keymap is consulted.
var keyAliases = map[string]string{
	"ctrl+h": "backspace", // BS (0x08): backspace on some terminals
}

var (
	inEditing = []Context{CtxEditor}
	inMain    = []Context{CtxEditor, CtxOutline}
)

// Keymap is the one table that drives both the dispatcher and the help bar.
// Every common-flow key is a ctrl key (or a function key): Option-as-Meta
// is awkward on a Mac, so alt keys are only hidden aliases.
var Keymap = []Binding{
	// Editor: the help bar, in nano's column order (pairs top and bottom).
	{Keys: []string{"ctrl+g", "f1"}, Key: "^G", Label: "Help", Desc: "list every key binding", Action: ActHelp, In: inMain, Bar: true},
	{Keys: []string{"ctrl+x"}, Key: "^X", Label: "Exit", Desc: "exit; asks to save a modified buffer", Action: ActExit, In: inMain, Bar: true},
	{Keys: []string{"ctrl+o"}, Key: "^O", Label: "Save", Desc: "write out: save the file", Action: ActWriteOut, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+q"}, Key: "^Q", Label: "Open", Desc: "quick open: fuzzy-find anchors, sections, files, and the operators of TLA+ models", Action: ActQuickOpen, In: inMain, Bar: true},
	{Keys: []string{"ctrl+w"}, Key: "^W", Label: "Where Is", Desc: "find text; Enter on an empty answer finds the last search again", Action: ActWhereIs, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+p"}, Key: "^P", Label: "Propose", Desc: "propose, without a prompt: on a diagnostic, fix it (a mechanical fix at once, ^Z undoes; a model fix to the sidebar); in an anchor changed vs HEAD, check it; outside every anchor, check every changed anchor (findings go to the sidebar)", Action: ActPropose, In: inMain, Bar: true},
	{Keys: []string{"ctrl+k"}, Key: "^K", Label: "Cut", Desc: "cut the line, or the selection", Action: ActCut, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+u"}, Key: "^U", Label: "Paste", Desc: "paste (uncut) the cut text", Action: ActPaste, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+z", "alt+u"}, Key: "^Z", Label: "Undo", Desc: "undo", Action: ActUndo, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+y", "alt+e"}, Key: "^Y", Label: "Redo", Desc: "redo", Action: ActRedo, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+t", "f12"}, Key: "^T", Label: "Follow", Desc: "follow the link: jump to the $Name or § under the cursor; from a definition or a heading a TLA+ model cites, to the model; from a tag or a tagged declaration in a model, to the spec", Action: ActToTarget, In: []Context{CtxEditor, CtxPopup, CtxDiagPopup}, Bar: true},
	{Keys: []string{"ctrl+r", "f3"}, Key: "^R", Label: "Checks", Desc: "the commit flow: show and focus the Checks sidebar (findings, suggested edits, agents' proposals; A there opens the commit view) and, from a spec file, check in the background every changed anchor no check has seen yet; hide the sidebar when it has focus. The Checks label stays stable", Action: ActReview, In: inMain, Bar: true},
	{Keys: []string{"ctrl+b", "alt+left", "alt+,"}, Key: "^B", Label: "Back", Desc: "go back through the jump history", Action: ActBack, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+f"}, Key: "^F", Label: "Format", Desc: "format the buffer: wrap paragraphs, strip trailing spaces (spec fmt); one undo step", Action: ActFormat, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+c", "alt+6"}, Key: "^C", Label: "Copy", Desc: "copy the selection (also to the system clipboard); with none, cancel (and stop a running check or a wait for .spec/lock)", Action: ActCopy, In: inEditing, Bar: true},
	{Keys: []string{"ctrl+n"}, Key: "^N", Label: "Analyze", Desc: "analyze the edits since HEAD for inconsistencies; findings go to the Checks sidebar", Action: ActAnalyze, In: inMain, Bar: true},
	// The outline toggle: ctrl+6 sends 0x1E (ctrl+^) in Terminal.app and
	// xterm; a terminal with the kitty keyboard protocol may report
	// ctrl+6 or ctrl+shift+6. One line, so the key is easy to change.
	{Keys: []string{"ctrl+^", "ctrl+6", "ctrl+shift+6", "ctrl+shift+^", "f2", "alt+o"}, Key: "^^", Label: "Outline", Desc: "show and focus the outline, or hide it when it has focus (ctrl+6; F2 and M-O too)", Action: ActOutline, In: inMain, Bar: true},
	// Focus: the next visible pane (editor, sidebar, outline). Terminal.app
	// sends ESC [ Z for shift+Tab; Tab does the same in the side panes
	// (in the editor it indents).
	{Keys: []string{"shift+tab"}, Key: "⇧Tab", Label: "Next Pane", Desc: "focus the next visible pane: editor, Checks sidebar, outline, editor (hidden panes are skipped)", Action: ActNextPane, In: inMain, Bar: true},
	{Keys: []string{"tab"}, Key: "Tab", Label: "Next Pane", Desc: "focus the next visible pane, as shift+Tab does", Action: ActNextPane, In: []Context{CtxOutline, CtxChecks}},

	// Editor: on the help screen only.
	{Keys: []string{"alt+right", "alt+."}, Key: "M-Right", Label: "Forward", Desc: "go forward through the jump history", Action: ActForward, In: inEditing},
	{Keys: []string{"ctrl+\\", "alt+r", "shift+f12"}, Key: "^\\", Label: "Refs", Desc: "list references to the target under the cursor, tags in TLA+ models too", Action: ActRefs, In: inEditing},
	{Keys: []string{"ctrl+l"}, Key: "^L", Label: "Look", Desc: "show the hover popup at the cursor: a $Name or § and the TLA+ models that cite it, or what a tag in a model cites", Action: ActLook, In: inEditing},
	{Keys: []string{"ctrl+]", "f8"}, Key: "^]", Label: "Next Edit", Desc: "jump to the next edit: lines changed since HEAD in any spec file, unsaved ones too; wraps at the end", Action: ActNextEdit, In: inEditing},
	{Keys: []string{"shift+f8", "f18"}, Key: "Sh-F8", Label: "Prev Edit", Desc: "jump to the previous edit (F18 too: Terminal.app sends it for shift+F8)", Action: ActPrevEdit, In: inEditing},

	{Keys: []string{"ctrl+_", "ctrl+/", "alt+g"}, Key: "^_", Label: "Go To Line", Desc: "go to a line number", Action: ActGoToLine, In: inEditing},
	{Keys: []string{"ctrl+a", "home"}, Key: "^A", Label: "Line Start", Desc: "go to the start of the line", Action: ActLineStart, In: inEditing},
	{Keys: []string{"ctrl+e", "end"}, Key: "^E", Label: "Line End", Desc: "go to the end of the line", Action: ActLineEnd, In: inEditing},
	{Keys: []string{"pgup"}, Key: "PgUp", Label: "Prev Page", Desc: "page up (fn+up on a Mac keyboard)", Action: ActPageUp, In: inMain},
	{Keys: []string{"ctrl+v", "pgdown"}, Key: "^V", Label: "Next Page", Desc: "page down (also PgDn, fn+down on a Mac keyboard)", Action: ActPageDown, In: inMain},
	{Keys: []string{"right"}, Key: "Right", Label: "Right", Desc: "move right one character", Action: ActRight, In: inEditing},
	{Keys: []string{"left"}, Key: "Left", Label: "Left", Desc: "move left one character", Action: ActLeft, In: inEditing},
	{Keys: []string{"up"}, Key: "Up", Label: "Prev Line", Desc: "move up one line", Action: ActUp, In: inMain},
	{Keys: []string{"down"}, Key: "Down", Label: "Next Line", Desc: "move down one line", Action: ActDown, In: inMain},
	{Keys: []string{"ctrl+home", "alt+\\"}, Key: "^Home", Label: "First Line", Desc: "go to the top of the file", Action: ActTop, In: inEditing},
	{Keys: []string{"ctrl+end", "alt+/"}, Key: "^End", Label: "Last Line", Desc: "go to the bottom of the file", Action: ActBottom, In: inEditing},
	{Keys: []string{"ctrl+left"}, Key: "^Left", Label: "Prev Word", Desc: "move to the previous word", Action: ActWordLeft, In: inEditing},
	{Keys: []string{"ctrl+right"}, Key: "^Right", Label: "Next Word", Desc: "move to the next word", Action: ActWordRight, In: inEditing},
	{Keys: []string{"shift+left"}, Key: "Sh-Left", Label: "Select", Desc: "select left", Action: ActSelLeft, In: inEditing},
	{Keys: []string{"shift+right"}, Key: "Sh-Right", Label: "Select", Desc: "select right", Action: ActSelRight, In: inEditing},
	{Keys: []string{"shift+up"}, Key: "Sh-Up", Label: "Select", Desc: "select up", Action: ActSelUp, In: inEditing},
	{Keys: []string{"shift+down"}, Key: "Sh-Down", Label: "Select", Desc: "select down", Action: ActSelDown, In: inEditing},
	{Keys: []string{"shift+home"}, Key: "Sh-Home", Label: "Select", Desc: "select to the line start", Action: ActSelLineStart, In: inEditing},
	{Keys: []string{"shift+end"}, Key: "Sh-End", Label: "Select", Desc: "select to the line end", Action: ActSelLineEnd, In: inEditing},
	{Keys: []string{"ctrl+space", "ctrl+@", "alt+a"}, Key: "^Space", Label: "Set Mark", Desc: "set or clear the mark (start a selection; shift+arrows select too). ctrl+6 is the outline now; M-A still sets the mark", Action: ActMark, In: inEditing},
	{Keys: []string{"ctrl+d", "delete"}, Key: "^D", Label: "Delete", Desc: "delete the character at the cursor", Action: ActDeleteChar, In: inEditing},
	{Keys: []string{"backspace"}, Key: "Bsp", Label: "Backspace", Desc: "delete the character before the cursor", Action: ActBackspace, In: inEditing},
	{Keys: []string{"enter"}, Key: "Enter", Label: "Newline", Desc: "insert a line break", Action: ActNewline, In: inEditing},
	{Keys: []string{"tab"}, Key: "Tab", Label: "Indent", Desc: "insert spaces", Action: ActTab, In: inEditing},
	{Keys: []string{"alt+x"}, Key: "M-X", Label: "Help Bar", Desc: "show or hide this help bar", Action: ActHelpBar, In: inMain},
	{Keys: []string{"esc"}, Key: "Esc", Label: "Clear", Desc: "clear the mark and messages", Action: ActCancel, In: inEditing},

	// Outline.
	{Keys: []string{"enter"}, Key: "Enter", Label: "Go To", Desc: "jump to the selected entry", Action: ActAccept, In: []Context{CtxOutline}, Bar: true},
	{Keys: []string{"esc"}, Key: "Esc", Label: "Editor", Desc: "return to the editor, keeping the outline", Action: ActCancel, In: []Context{CtxOutline}, Bar: true},

	// Popup. ^C is not bound here: it closes the popup and acts in the
	// editor (copy a selection, or cancel), as any other unbound key does.
	{Keys: []string{"ctrl+p"}, Key: "^P", Label: "Fix", Desc: "propose a fix of the diagnostic (the most severe one shown) for review", Action: ActFix, In: []Context{CtxDiagPopup}, Bar: true},
	{Keys: []string{"esc"}, Key: "Esc", Label: "Close", Desc: "close the popup (^C too)", Action: ActCancel, In: []Context{CtxPopup, CtxDiagPopup}, Bar: true},

	// List.
	{Keys: []string{"enter"}, Key: "Enter", Label: "Go To", Desc: "jump to the selected entry", Action: ActAccept, In: []Context{CtxList}, Bar: true},
	{Keys: []string{"ctrl+c", "esc"}, Key: "^C", Label: "Cancel", Desc: "close the list", Action: ActCancel, In: []Context{CtxList}, Bar: true},
	{Keys: []string{"up", "ctrl+p"}, Key: "Up", Label: "Prev", Desc: "select the previous entry", Action: ActUp, In: []Context{CtxList}, Bar: true},
	{Keys: []string{"down", "ctrl+n"}, Key: "Down", Label: "Next", Desc: "select the next entry", Action: ActDown, In: []Context{CtxList}, Bar: true},
	{Keys: []string{"backspace"}, Key: "Bsp", Label: "Erase", Desc: "erase the last character of the filter", Action: ActBackspace, In: []Context{CtxList, CtxPrompt}},

	// Prompt.
	{Keys: []string{"enter"}, Key: "Enter", Label: "Accept", Desc: "accept the answer", Action: ActAccept, In: []Context{CtxPrompt}, Bar: true},
	{Keys: []string{"ctrl+c", "esc"}, Key: "^C", Label: "Cancel", Desc: "cancel the prompt", Action: ActCancel, In: []Context{CtxPrompt}, Bar: true},

	// Save on exit.
	{Keys: []string{"y", "Y"}, Key: "Y", Label: "Yes", Desc: "save, then exit", Action: ActYes, In: []Context{CtxConfirm}, Bar: true},
	{Keys: []string{"n", "N"}, Key: "N", Label: "No", Desc: "exit without saving", Action: ActNo, In: []Context{CtxConfirm}, Bar: true},
	{Keys: []string{"ctrl+c", "esc"}, Key: "^C", Label: "Cancel", Desc: "keep editing", Action: ActCancel, In: []Context{CtxConfirm}, Bar: true},

	// File changed on disk while modified.
	{Keys: []string{"k", "K"}, Key: "K", Label: "Keep Mine", Desc: "keep the buffer; the next save overwrites the file", Action: ActKeepMine, In: []Context{CtxConflict}, Bar: true},
	{Keys: []string{"t", "T"}, Key: "T", Label: "Take Theirs", Desc: "discard the buffer and load the file from disk", Action: ActTakeTheirs, In: []Context{CtxConflict}, Bar: true},
	{Keys: []string{"d", "D"}, Key: "D", Label: "Show Diff", Desc: "show the buffer against the file on disk", Action: ActShowDiff, In: []Context{CtxConflict}, Bar: true},
	{Keys: []string{"ctrl+c", "esc"}, Key: "^C", Label: "Later", Desc: "decide later: keep editing; the next save asks again", Action: ActCancel, In: []Context{CtxConflict}, Bar: true},

	// Completion: other keys close it and act in the editor; typing
	// filters.
	{Keys: []string{"tab", "enter"}, Key: "Tab", Label: "Insert", Desc: "insert the selected name (Enter too); # becomes §", Action: ActAccept, In: []Context{CtxCompletion}, Bar: true},
	{Keys: []string{"esc"}, Key: "Esc", Label: "Close", Desc: "close the completion list", Action: ActCancel, In: []Context{CtxCompletion}, Bar: true},
	{Keys: []string{"up"}, Key: "Up", Label: "Prev", Desc: "select the previous entry", Action: ActUp, In: []Context{CtxCompletion}, Bar: true},
	{Keys: []string{"down"}, Key: "Down", Label: "Next", Desc: "select the next entry", Action: ActDown, In: []Context{CtxCompletion}, Bar: true},
	{Keys: []string{"backspace"}, Key: "Bsp", Label: "Erase", Desc: "erase the last character typed", Action: ActBackspace, In: []Context{CtxCompletion}},

	// Checks sidebar: the review screen's letters.
	{Keys: []string{"y", "Y"}, Key: "y", Label: "Accept", Desc: "apply the selected fix into the buffer (on a problem: its pending fixes; on a check's title: all of them; an agent's proposal: all of it); nothing is saved or committed (^Z undoes), and it is recorded as applied when the file is saved. On a fix the text already holds but not only at its place: record it as done by hand (a clear one is recorded on its own)", Action: ActAcceptHunk, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"n", "N"}, Key: "n", Label: "Dismiss", Desc: "skip the selected fix, or dismiss the problem (and the fixes only it wants) or question (an agent's proposal: reject it, with a reason)", Action: ActSkipHunk, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"e"}, Key: "e", Label: "Edit", Desc: "edit the fix's replacement, then apply it", Action: ActEditHunk, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"f"}, Key: "f", Label: "Feedback", Desc: "send feedback to the model, which redoes the fix; on a question of a check or an analysis: answer it, and the model rethinks it (its new fixes, and any change to your own edit, are proposed, not applied)", Action: ActFeedback, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"enter"}, Key: "Enter", Label: "Go To", Desc: "go to the card's place in the editor: a fix's place, shown there as it would read (the sidebar keeps it selected; ^R comes back); a problem's: your edit; an agent's proposal: review it edit by edit. A click on a card selects it and shows its place the same way", Action: ActAccept, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"c"}, Key: "c", Label: "Message", Desc: "write the commit message", Action: ActMessage, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"A"}, Key: "A", Label: "Commit", Desc: "open the commit view: tick the changes to commit (after saving modified buffers)", Action: ActCommitView, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"ctrl+r", "f3"}, Key: "^R", Label: "Hide", Desc: "hide the sidebar", Action: ActReview, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"ctrl+f"}, Key: "^F", Label: "Format", Desc: "accept the selected card formatted: preview the edit with the format (as ^F formats the buffer); y then applies both as one undo step. ^F again: as it is", Action: ActFormat, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"r"}, Key: "r", Label: "Removed", Desc: "show or hide the words removed since HEAD, as ghost text where they were (this session only)", Action: ActShowRemoved, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"up", "k"}, Key: "Up", Label: "Prev", Desc: "select the previous card", Action: ActUp, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"down", "j"}, Key: "Down", Label: "Next", Desc: "select the next card", Action: ActDown, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"esc"}, Key: "Esc", Label: "Editor", Desc: "return to the editor, keeping the sidebar", Action: ActCancel, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"shift+tab"}, Key: "⇧Tab", Label: "Next Pane", Desc: "focus the next visible pane: outline, or editor", Action: ActNextPane, In: []Context{CtxChecks}, Bar: true},
	{Keys: []string{"ctrl+o"}, Key: "^O", Label: "Save", Desc: "write out: save the current file", Action: ActWriteOut, In: []Context{CtxChecks}},
	{Keys: []string{"ctrl+g", "f1"}, Key: "^G", Label: "Help", Desc: "list every key binding", Action: ActHelp, In: []Context{CtxChecks}},
	{Keys: []string{"ctrl+x"}, Key: "^X", Label: "Exit", Desc: "exit; asks to save a modified buffer", Action: ActExit, In: []Context{CtxChecks}},

	// Commit view.
	{Keys: []string{"y", "Y"}, Key: "y", Label: "Include", Desc: "include the selected change in the commit", Action: ActYes, In: []Context{CtxCommit}, Bar: true},
	{Keys: []string{"n", "N"}, Key: "n", Label: "Exclude", Desc: "leave the selected change in the files, uncommitted", Action: ActNo, In: []Context{CtxCommit}, Bar: true},
	{Keys: []string{"space"}, Key: "Space", Label: "Toggle", Desc: "tick or untick the selected change", Action: ActToggle, In: []Context{CtxCommit}, Bar: true},
	{Keys: []string{"c"}, Key: "c", Label: "Message", Desc: "edit the commit message", Action: ActMessage, In: []Context{CtxCommit}, Bar: true},
	{Keys: []string{"A"}, Key: "A", Label: "Commit", Desc: "commit the ticked changes (hooks run; the trailers are added)", Action: ActCommitView, In: []Context{CtxCommit}, Bar: true},
	{Keys: []string{"ctrl+x", "esc", "q"}, Key: "Esc", Label: "Close", Desc: "close without committing", Action: ActCancel, In: []Context{CtxCommit}, Bar: true},
	{Keys: []string{"up", "k"}, Key: "Up", Label: "Prev", Desc: "select the previous change", Action: ActUp, In: []Context{CtxCommit}, Bar: true},
	{Keys: []string{"down", "j"}, Key: "Down", Label: "Next", Desc: "select the next change", Action: ActDown, In: []Context{CtxCommit}, Bar: true},

	// Review screen.
	{Keys: []string{"y", "Y"}, Key: "y", Label: "Accept", Desc: "accept the selected hunk", Action: ActAcceptHunk, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"n", "N"}, Key: "n", Label: "Skip", Desc: "skip the selected hunk (the change itself cannot be skipped: R rejects)", Action: ActSkipHunk, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"f"}, Key: "f", Label: "Feedback", Desc: "send feedback to the model, which redoes the selected hunk", Action: ActFeedback, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"e"}, Key: "e", Label: "Edit", Desc: "edit the selected hunk's replacement text", Action: ActEditHunk, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"A"}, Key: "A", Label: "Apply", Desc: "apply the required primary and selected hunks into the editor buffers; changes remain unsaved", Action: ActApprove, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"R"}, Key: "R", Label: "Reject", Desc: "reject the whole proposal, with a reason", Action: ActRejectAll, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"ctrl+x", "esc", "q"}, Key: "Esc", Label: "Close", Desc: "close the review; the proposal stays pending", Action: ActCancel, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"up", "k"}, Key: "Up", Label: "Prev", Desc: "select the previous hunk", Action: ActUp, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"down", "j"}, Key: "Down", Label: "Next", Desc: "select the next hunk", Action: ActDown, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"pgup"}, Key: "PgUp", Label: "Prev Page", Desc: "scroll up", Action: ActPageUp, In: []Context{CtxReview}},
	{Keys: []string{"ctrl+v", "pgdown", "space"}, Key: "^V", Label: "Next Page", Desc: "scroll down", Action: ActPageDown, In: []Context{CtxReview}},

	// The text box (a hunk's replacement, the commit message, feedback): a
	// modal over the screen; the editor's editing keys apply too.
	{Keys: []string{"ctrl+o"}, Key: "^O", Label: "Keep", Desc: "keep the text (a replacement is then applied; feedback or an answer is sent)", Action: ActDone, In: []Context{CtxHunkEdit}, Bar: true},
	{Keys: []string{"esc", "ctrl+x"}, Key: "Esc", Label: "Cancel", Desc: "discard the text and close the box (^X too)", Action: ActCancel, In: []Context{CtxHunkEdit}, Bar: true},

	// Save before a check.
	{Keys: []string{"y", "Y"}, Key: "Y", Label: "Save", Desc: "save every modified buffer, then run the check", Action: ActYes, In: []Context{CtxSaveFirst}, Bar: true},
	{Keys: []string{"ctrl+c", "esc", "n", "N"}, Key: "^C", Label: "Cancel", Desc: "do not run the check", Action: ActCancel, In: []Context{CtxSaveFirst}, Bar: true},

	// A notice: any key closes it (Esc is the one shown on the help screen).
	{Keys: []string{"esc"}, Key: "Any key", Label: "Close", Desc: "close the message; every key does", Action: ActCancel, In: []Context{CtxNotice}, Bar: true},

	{Keys: []string{"r"}, Key: "r", Label: "Recheck", Desc: "recheck this proposal against current files and buffers", Action: ActRetryCheck, In: []Context{CtxReview}, Bar: true},
	{Keys: []string{"r"}, Key: "r", Label: "Refresh", Desc: "refresh the saved changes before selecting a commit", Action: ActRefreshSelection, In: []Context{CtxCommit}},
	{Keys: []string{"ctrl+t"}, Key: "^T", Label: "Settings", Desc: "open settings", Action: ActSettings, In: []Context{CtxHelp}, Bar: true},
	{Keys: []string{"ctrl+w"}, Key: "^W", Label: "Find", Desc: "find a command", Action: ActWhereIs, In: []Context{CtxHelp}, Bar: true},
	{Keys: []string{"enter"}, Key: "Ret", Label: "Next match", Desc: "visit the next search match", Action: ActAccept, In: []Context{CtxHelp}, Bar: true},
	{Keys: []string{"ctrl+o"}, Key: "^O", Label: "Save", Desc: "save staged settings", Action: ActWriteOut, In: []Context{CtxSettings}, Bar: true},
	{Keys: []string{"ctrl+r"}, Key: "^R", Label: "Revert", Desc: "revert staged settings", Action: ActRevertSettings, In: []Context{CtxSettings}, Bar: true},
	{Keys: []string{"ctrl+t"}, Key: "^T", Label: "Test", Desc: "review a connection test plan", Action: ActTestSettings, In: []Context{CtxSettings}, Bar: true},
	{Keys: []string{"ctrl+q"}, Key: "^Q", Label: "Sections", Desc: "choose a settings section", Action: ActSections, In: []Context{CtxSettings}, Bar: true},
	{Keys: []string{"esc"}, Key: "Esc", Label: "Back", Desc: "return without losing staged settings", Action: ActCancel, In: []Context{CtxSettings}, Bar: true},
	{Keys: []string{"tab"}, Key: "Tab", Label: "Next field", Desc: "focus next field", Action: ActTab, In: []Context{CtxSettings}, Bar: true},
	{Keys: []string{"shift+tab"}, Key: "⇧Tab", Label: "Previous", Desc: "focus previous field", Action: ActNextPane, In: []Context{CtxSettings}, Bar: true},

	// Help screen.
	{Keys: []string{"ctrl+x", "esc", "ctrl+c", "q"}, Key: "^X", Label: "Exit Help", Desc: "close the help screen", Action: ActCancel, In: []Context{CtxHelp}, Bar: true},
	{Keys: []string{"pgup"}, Key: "PgUp", Label: "Prev Page", Desc: "page up", Action: ActPageUp, In: []Context{CtxHelp}, Bar: true},
	{Keys: []string{"ctrl+v", "pgdown"}, Key: "^V", Label: "Next Page", Desc: "page down", Action: ActPageDown, In: []Context{CtxHelp}, Bar: true},
	{Keys: []string{"up"}, Key: "Up", Label: "Prev Line", Desc: "scroll up", Action: ActUp, In: []Context{CtxHelp}},
	{Keys: []string{"down"}, Key: "Down", Label: "Next Line", Desc: "scroll down", Action: ActDown, In: []Context{CtxHelp}},
}

// enabled says, per action, whether it can run now in ctx; an action
// without an entry always can. The help bar draws an entry whose action
// cannot run dimmed, in its usual slot. Disabled keys stay inert;
// Review and Help expose the reason without dispatching a handler. A predicate is drawn on every frame: it reads
// the current state and changes nothing.
var enabled = map[Action]func(a *App, ctx Context) bool{
	ActToTarget: func(a *App, ctx Context) bool {
		if ctx == CtxPopup || ctx == CtxDiagPopup {
			return a.popup != nil && (a.popup.target != nil || len(a.popup.choices) > 0)
		}
		return a.tokenIn(a.cur.Ed.Cursor(), true).any()
	},
	ActBack:    func(a *App, _ Context) bool { return len(a.back) > 0 },
	ActForward: func(a *App, _ Context) bool { return len(a.fwd) > 0 },
	ActPropose: func(a *App, _ Context) bool { return a.job == nil && a.proposeBlocked() == "" },
	ActFix:     func(a *App, _ Context) bool { return a.job == nil && !a.inModel() },
	ActAnalyze: func(a *App, _ Context) bool { return a.job == nil && !a.inModel() },

	ActCut: func(a *App, _ Context) bool { return a.cur.Ed.CanCut() },
	ActCopy: func(a *App, _ Context) bool {
		_, sel := a.cur.Ed.Selection()
		return sel || a.cur.Ed.Marked() || a.job != nil || a.wait != nil
	},
	ActPaste: func(a *App, _ Context) bool { return a.cutBuf != "" },
	ActUndo:  func(a *App, _ Context) bool { return a.cur.Ed.Buffer().CanUndo() },
	ActRedo:  func(a *App, _ Context) bool { return a.cur.Ed.Buffer().CanRedo() },
	ActAccept: func(a *App, ctx Context) bool {
		switch ctx {
		case CtxList:
			if a.list == nil {
				return false
			}
			_, ok := a.list.selected()
			return ok
		case CtxChecks:
			_, ok := a.cardLoc(a.side.selected())
			c := a.side.selected()
			return ok || c != nil && c.kind == cardProposal
		}
		return true
	},
	ActAcceptHunk: func(a *App, ctx Context) bool {
		if ctx == CtxChecks {
			return a.cardBlocked(a.side.selected()) == ""
		}
		r, ok := a.top().(*reviewScreen)
		if !ok {
			return false
		}
		n := r.selected()
		_, stale := r.stale[n]
		return n > 0 && r.gone == "" && !r.p.ChecksBlocking() && !r.isPrimary(n) && !stale && !r.p.AppliedHunk(n) && r.state[n] != hunkAccepted
	},
	ActSkipHunk: func(a *App, ctx Context) bool {
		if ctx == CtxChecks {
			return a.side.selected() != nil
		}
		r, ok := a.top().(*reviewScreen)
		return ok && r.gone == "" && !r.p.ChecksBlocking() && r.selected() > 0 && !r.isPrimary(r.selected()) && !r.p.AppliedHunk(r.selected()) && r.state[r.selected()] != hunkSkipped
	},
	ActFeedback: func(a *App, ctx Context) bool {
		if ctx == CtxChecks {
			sel := a.side.selected()
			if sel != nil && sel.kind == cardQuestion {
				return a.job == nil && answerable(sel.p)
			}
			c := a.side.fixCard(sel)
			return a.job == nil && c != nil
		}
		r, ok := a.top().(*reviewScreen)
		return ok && a.job == nil && a.wait == nil && r.gone == "" && !r.p.ChecksBlocking() && !r.p.IsModel() && r.selected() > 0 && !r.isPrimary(r.selected()) && !r.p.AppliedHunk(r.selected())
	},
	ActEditHunk: func(a *App, ctx Context) bool {
		if ctx == CtxChecks {
			c := a.side.fixCard(a.side.selected())
			return c != nil && a.fixState(c.p, c.hunk) == fixPending && a.cardBlocked(c) == "" && a.job == nil && a.wait == nil
		}
		r, ok := a.top().(*reviewScreen)
		if !ok {
			return false
		}
		n := r.selected()
		_, stale := r.stale[n]
		return n > 0 && r.gone == "" && !r.p.ChecksBlocking() && !r.isPrimary(n) && !stale && !r.p.AppliedHunk(n)
	},
	ActApprove: func(a *App, _ Context) bool {
		r, ok := a.top().(*reviewScreen)
		return ok && a.job == nil && a.wait == nil && r.approveBlocked(a) == ""
	},
	ActCommitView: func(a *App, ctx Context) bool {
		if ctx == CtxCommit {
			s, ok := a.top().(*commitScreen)
			return ok && a.job == nil && a.wait == nil && s.commitBlocked(a) == ""
		}
		return !a.inModel()
	},
	ActYes: func(a *App, ctx Context) bool {
		if s, ok := a.top().(*commitScreen); ok && ctx == CtxCommit {
			c := s.selected()
			return c != nil && a.job == nil && a.wait == nil && !s.ticked[c.ID]
		}
		return true
	},
	ActNo: func(a *App, ctx Context) bool {
		if s, ok := a.top().(*commitScreen); ok && ctx == CtxCommit {
			c := s.selected()
			return c != nil && a.job == nil && a.wait == nil && s.ticked[c.ID]
		}
		return true
	},
	ActRetryCheck: func(a *App, _ Context) bool { r, ok := a.top().(*reviewScreen); return ok && r.retryBlocked(a) == "" },
	ActRefreshSelection: func(a *App, _ Context) bool {
		s, ok := a.top().(*commitScreen)
		return ok && s.refreshNeeded && a.job == nil && a.wait == nil
	},
	ActToggle: func(a *App, ctx Context) bool {
		if ctx == CtxCommit {
			s, ok := a.top().(*commitScreen)
			return ok && s.selected() != nil && a.job == nil && a.wait == nil
		}
		return true
	},
	ActFormat: func(a *App, ctx Context) bool {
		if ctx == CtxChecks {
			c := a.side.selected()
			if c == nil || a.job != nil || a.wait != nil {
				return false
			}
			if a.side.format[c.id()] {
				return true
			}
			if c.kind == cardProposal {
				return !c.p.IsModel() && a.cardBlocked(c) == ""
			}
			fix := a.side.fixCard(c)
			return fix != nil && a.cardBlocked(fix) == ""
		}
		if ctx == CtxReview {
			r, ok := a.top().(*reviewScreen)
			return ok && r.gone == "" && !r.p.ChecksBlocking() && a.job == nil && a.wait == nil
		}
		return !a.inModel()
	},
	ActRejectAll: func(a *App, _ Context) bool {
		r, ok := a.top().(*reviewScreen)
		return ok && a.wait == nil && r.gone == ""
	},
}

// labels gives, per action, the help bar label for the current state; an
// action without an entry shows its binding's Label. Like enabled, a
// labeller is drawn on every frame and changes nothing.
var labels = map[Action]func(a *App, ctx Context, b Binding) string{
	ActAcceptHunk: func(a *App, ctx Context, b Binding) string {
		if c := a.side.selected(); ctx == CtxChecks && c != nil && (c.kind == cardGroup || c.kind == cardProblem && len(c.prob.fixes) > 1) {
			return "Accept All"
		}
		return b.Label
	},
	ActSkipHunk: func(a *App, ctx Context, b Binding) string {
		if c := a.side.selected(); ctx == CtxChecks && c != nil && c.kind == cardEdit {
			return "Skip"
		}
		return b.Label
	},
}

// labelOf returns b's help bar label in ctx now.
func (a *App) labelOf(ctx Context, b Binding) string {
	if f, ok := labels[b.Action]; ok {
		return f(a, ctx, b)
	}
	return b.Label
}

// canRun reports whether b's action can run now in ctx (see enabled).
func (a *App) canRun(ctx Context, b Binding) bool {
	if custom, ok := a.top().(interface{ helperEnabled(*App, Binding) bool }); ok && !custom.helperEnabled(a, b) {
		return false
	}
	if a.job != nil && a.job.irreversible && (b.Action == ActExit || ctx == CtxCommit && b.Action != ActHelp) {
		return false
	}
	if h, ok := a.top().(*hunkEditScreen); ok && ctx == CtxHunkEdit {
		switch b.Action {
		case ActUndo:
			return h.ed.Buffer().CanUndo()
		case ActRedo:
			return h.ed.Buffer().CanRedo()
		case ActPaste:
			return a.cutBuf != ""
		case ActCut:
			return h.ed.CanCut()
		}
	}

	f, ok := enabled[b.Action]
	return !ok || f(a, ctx)
}

// keyIndex maps a context and a key string to a binding.
var keyIndex = buildKeyIndex()

func buildKeyIndex() map[Context]map[string]Binding {
	idx := map[Context]map[string]Binding{}
	for _, b := range Keymap {
		for _, c := range b.In {
			if idx[c] == nil {
				idx[c] = map[string]Binding{}
			}
			for _, k := range b.Keys {
				if _, dup := idx[c][k]; !dup {
					idx[c][k] = b
				}
			}
		}
	}
	return idx
}

// Lookup returns the binding of key in ctx.
func Lookup(ctx Context, key string) (Binding, bool) {
	b, ok := keyIndex[ctx][key]
	return b, ok
}

// BindingsIn returns the bindings that apply in ctx, in table order.
func BindingsIn(ctx Context) []Binding {
	var out []Binding
	for _, b := range Keymap {
		if slices.Contains(b.In, ctx) {
			out = append(out, b)
		}
	}
	return out
}

// BarBindings returns the bindings the help bar shows in ctx, in order.
func BarBindings(ctx Context) []Binding {
	var out []Binding
	for _, b := range BindingsIn(ctx) {
		if b.Bar {
			out = append(out, b)
		}
	}
	return out
}

// HelpBarLines lays out the help bar for ctx as two plain lines of width
// cells: entries in columns, filled top then bottom, nano style. It returns
// the cells as (key, label) pairs per line so the caller can style them.
func HelpBarLines(ctx Context, width int) [2][]barCell {
	return layoutBar(BarBindings(ctx), width)
}

// layoutBar lays out the help bar entries bs, as HelpBarLines does.
// Columns are as wide as six would be, so short bars line up with long
// ones (nano does the same); when that would cut a label, each column is
// as wide as its widest entry, and the room left is shared out.
func layoutBar(bs []Binding, width int) [2][]barCell {
	var out [2][]barCell
	if len(bs) == 0 {
		return out
	}
	cols := max((len(bs)+1)/2, 6)
	need := make([]int, cols) // per column: the widest entry and a space
	for i, b := range bs {
		need[i/2] = max(need[i/2], ansi.StringWidth(b.Key)+1+ansi.StringWidth(b.Label)+1)
	}
	colW := max(width/cols, 1)
	widths := make([]int, cols)
	equal := slices.Max(need) <= colW
	sum := 0
	for c := range cols {
		sum += need[c]
	}
	for c := range cols {
		widths[c] = colW
		if !equal && sum <= width {
			widths[c] = need[c] + (width-sum)/cols
		}
	}
	xs := make([]int, cols)
	for c := 1; c < cols; c++ {
		xs[c] = xs[c-1] + widths[c-1]
	}
	for i, b := range bs {
		row, col := i%2, i/2
		out[row] = append(out[row], barCell{Key: b.Key, Label: b.Label, X: xs[col], W: widths[col]})
	}
	return out
}

// barCell is one help bar entry at column X, W cells wide.
type barCell struct {
	Key, Label string
	X, W       int
}

// keyNames formats the keys of a binding for the help screen.
func keyNames(b Binding) string {
	return strings.Join(b.Keys, ", ")
}
