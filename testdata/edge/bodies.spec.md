# Bodies

**$Body.Plain.** A single paragraph
spanning two lines.

**$Body.List.** Followed directly by a list:
- one
- two

**$Body.Loose.** Followed directly by a loose list:
- one

- two

  A continued paragraph in item two.

This paragraph is not in the body.

**$Body.Ordered.** An ordered list starting at 2 cannot interrupt a paragraph:
2. so this line is paragraph text
3. and so is this one

**$Body.OrderedOne.** An ordered list starting at 1 can:
1. first
2. second

**$Body.Lazy.** Lazy continuation:
- item
lazy line belongs to the item
- item two

**$Body.Quote.** Followed by a blockquote:
> quoted
lazy quoted line
> more

**$Body.Fence.** Followed by a fence, which is not part of the body:
```go
x := 1
```

**$Body.Blank.** Blank-line separation:

- not in the body

**$Body.Code.** A code span `a  b` keeps its spaces; other   runs
   collapse.

**$Body.ListCode.** A list whose last item ends with a fence:
- item

  ```
  code in the item
  ```

The end.
