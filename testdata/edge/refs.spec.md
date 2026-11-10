# References

**$Frame.Timing (frame timing).** Every frame is timed; see $Frame.Timing itself.

**Def $Time.Media (media time).** Media time is counted in ticks.

## Escapes and sigils

An escaped \$Frame.Timing is literal text. A double backslash \\$Time.Media is an
escaped backslash followed by a reference.

Shell and money: $HOME $PATH $5.00 $1 $USD are not references.

Preceded by a word character: a$Frame.Timing, x_$Frame.Timing and $$Frame.Timing are
not references.

Preceded by punctuation: ($Frame.Timing), "$Time.Media", /$Frame.Timing, *$Time.Media*,
_$Frame.Timing_ and **$Time.Media** mid-sentence are references.

## Maximal munch

The candidate in $Frame.Timing. ends before the final period.
In $Frame.Timing's case the apostrophe ends it.
$Frame.timing is plain text, not `$Frame` followed by `.timing`.
$Frame_x is plain text too, and so is $Frame.Timing.x.
A dangling $Not.Defined is still a reference.

## Links and tables

Link text is not opaque: [$Frame.Timing](https://example.test/$Time.Media "title $Time.Media").
Autolinks are opaque: <https://example.test/$Frame.Timing>.
Image alt text is text: ![$Time.Media alt](img/$Frame.Timing.png).
Reference link: [$Frame.Timing][ref].

[ref]: https://example.test/$Time.Media "ref title $Time.Media"

| Name | Note |
|------|------|
| $Frame.Timing | see $Time.Media |
| `$Frame.Timing` | code |

## Adjacency hazard

Adjacent: $Frame.Timing/$Time.Media and $Frame.Timing ($Time.Media) on one line.
