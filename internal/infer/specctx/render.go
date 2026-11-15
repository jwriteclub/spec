// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package specctx

import (
	"fmt"
	"strings"
)

// Render writes the context as the text of a user message. Spec text is
// quoted between tags (<text>, <file>) and never reformatted, so a model
// can copy exact substrings from it.
func (c *Context) Render() string {
	var b strings.Builder
	if ch := c.Change; ch != nil && len(c.Batch) > 0 {
		all := append([]*Change{ch}, c.Batch...)
		fmt.Fprintf(&b, "## Proposed changes (one batch of %d, reviewed together)\n\n", len(all))
		b.WriteString("Every change of the batch is accepted or rejected as a unit. An anchor that one change adds\n")
		b.WriteString("exists for the others: a reference to it is not stale. Every changed anchor is the author's: never\n")
		b.WriteString("edit one of them.\n\n")
		for i, x := range all {
			fmt.Fprintf(&b, "### Change %d of %d: $%s\n\n", i+1, len(all), x.Name)
			writeChange(&b, x, "####")
		}
	} else if ch != nil {
		b.WriteString("## Proposed change\n\n")
		writeChange(&b, ch, "###")
	}
	if len(c.Regions) > 0 {
		b.WriteString("## Edited regions\n")
		for _, r := range c.Regions {
			fmt.Fprintf(&b, "\n### %s lines %d-%d\n\n", r.File, r.StartLine, r.EndLine)
			quote(&b, "now", "", r.Text)
			if r.Old != "" {
				quote(&b, "before", "", r.Old)
			}
		}
		b.WriteString("\n")
	}
	if len(c.Neighbors) > 0 {
		b.WriteString("## Related anchors (two hops of $Name references, either direction)\n")
		for _, n := range c.Neighbors {
			fmt.Fprintf(&b, "\n### $%s (%s, %s:%d, %s)\n\n", n.Name, n.Kind, n.File, n.Line, hopWord(n.Hops))
			quote(&b, "text", "", n.Text)
		}
		b.WriteString("\n")
	}
	if len(c.Mentions) > 0 && c.Change != nil {
		if len(c.Batch) > 0 {
			b.WriteString("## Prose that mentions the changed anchors\n")
		} else {
			fmt.Fprintf(&b, "## Prose that mentions $%s\n", c.Change.Name)
		}
		for _, m := range c.Mentions {
			fmt.Fprintf(&b, "\n### %s:%d (%s)\n\n", m.File, m.Line, m.Kind)
			quote(&b, "text", "", m.Text)
		}
		b.WriteString("\n")
	}
	if len(c.Index) > 0 {
		b.WriteString("## Anchor index\n\n")
		for _, e := range c.Index {
			fmt.Fprintf(&b, "- $%s (%s, %s): %s\n", e.Name, e.Kind, e.File, e.Summary)
		}
		b.WriteString("\n")
	}
	if len(c.Files) > 0 {
		b.WriteString("## Files\n\n")
		for _, f := range c.Files {
			if f.Full {
				quote(&b, "file", fmt.Sprintf(" path=%q", f.Path), f.Text)
				continue
			}
			for _, p := range f.Parts {
				quote(&b, "file", fmt.Sprintf(" path=%q excerpt=%q line=\"%d\"", f.Path, p.Label, p.Line), p.Text)
			}
		}
	}
	if len(c.Omitted) > 0 {
		b.WriteString("\n## Left out to fit the context budget\n\n")
		for _, o := range c.Omitted {
			b.WriteString("- " + o + "\n")
		}
	}
	return b.String()
}

// writeChange writes one change: its action, anchor, kind and texts, with
// its subheadings at level head.
func writeChange(b *strings.Builder, ch *Change, head string) {
	fmt.Fprintf(b, "action: %s\n", ch.Action)
	fmt.Fprintf(b, "anchor: $%s\n", ch.Name)
	fmt.Fprintf(b, "file: %s\n", ch.File)
	switch {
	case ch.OldKind != "" && ch.NewKind != "" && ch.OldKind != ch.NewKind:
		fmt.Fprintf(b, "kind: %s -> %s\n", ch.OldKind, ch.NewKind)
	case ch.NewKind != "":
		fmt.Fprintf(b, "kind: %s\n", ch.NewKind)
	case ch.OldKind != "":
		fmt.Fprintf(b, "kind: %s\n", ch.OldKind)
	}
	if ch.Where != "" {
		fmt.Fprintf(b, "where: %s\n", ch.Where)
	}
	fmt.Fprintf(b, "reason: %s\n", oneLine(ch.Why))
	if ch.OldText != "" {
		fmt.Fprintf(b, "\n%s Current text\n\n", head)
		quote(b, "text", "", ch.OldText)
	}
	if ch.NewText != "" {
		fmt.Fprintf(b, "\n%s Proposed text\n\n", head)
		quote(b, "text", "", ch.NewText)
	} else {
		fmt.Fprintf(b, "\n%s Proposed text\n\n(none: the anchor is removed)\n", head)
	}
	b.WriteString("\n")
}

// quote writes text between <tag attrs> and </tag> lines.
func quote(b *strings.Builder, tag, attrs, text string) {
	fmt.Fprintf(b, "<%s%s>\n", tag, attrs)
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "</%s>\n", tag)
}

func hopWord(h int) string {
	if h == 1 {
		return "1 hop"
	}
	return fmt.Sprintf("%d hops", h)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Tokens estimates the rendered size in tokens (bytes/4).
func (c *Context) Tokens() int {
	return tokens(len(c.Render()))
}
