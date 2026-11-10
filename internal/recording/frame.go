// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import "regexp"

// shownSeconds is a time the UI shows in tenths of a second ("0.3 s"): a
// stage's or a running job's wall-clock time, which differs a little on
// every run.
var shownSeconds = regexp.MustCompile(`\d+\.\d s\b`)

// SameFrame reports whether two plain frames are the same but for the
// times they show in seconds, which a replay cannot reproduce exactly.
func SameFrame(a, b string) bool {
	return shownSeconds.ReplaceAllString(a, "#.# s") == shownSeconds.ReplaceAllString(b, "#.# s")
}
