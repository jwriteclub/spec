# Definitions

**$Frame.Timing.** Plain invariant, period inside the bold.

**$Frame.Shape (segment shape).** With a title.

**Def $Time.Media.** A definition.

**Def $Time.Wall (wall time).** A definition with a title.

__$Frame.Under.__ Underscore strong.

**$Frame.NoPeriod** A label without a period is still a label.

**$Frame.Outside**. The period may sit outside the bold.

**$Frame.Titled (depends on $Time.Media).** A title with a reference.

Mid-sentence bold **$Frame.Timing** is a reference, and so is $Frame.Shape.

## Malformed

**$frame.timing.** Lowercase.

**Definition $Frame.Bad.** Wrong prefix.

**Def. $Frame.Bad.** Period after Def.

**$HTTP.** No lowercase letter.

**$Frame.Timing extra words.** Words after the name.

**$Frame.Timing** and **$Time.Media.** Only the first bold counts, and it is a valid label: this is a duplicate.

## Nested

- **$Frame.InList.** In a list item.

> **$Frame.InQuote.** In a blockquote.

| Cell |
|------|
| **$Frame.InCell.** |

- # $Frame.InListHeading

## Section form

### $Relay.OneSession: one session per camera

Body of a section-form anchor, referencing $Frame.Timing.

**$Relay.Inner.** Nested in a section-form body.

#### Sub heading inside the body

Still the body of $Relay.OneSession.

#### $Relay.Deeper: a nested section-form label

Also nested.

### Def $Relay.Term

A section-form definition without a title.

## Setext

**$Frame.Setext.**
---

The bold line above is a setext heading, not a paragraph, so it defines nothing.

$Frame.SetextForm: a setext section-form anchor
-----------------------------------------------

Its body runs to the end of the file.
