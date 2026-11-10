// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"bufio"
	"os"
	"strings"
	"sync"

	"github.com/golangci/misspell"
)

// SystemWordLists are the word lists Dictionary looks for, in order: the
// one macOS and most BSDs ship (web2, 235,976 words) and the paths Linux
// distributions use when a words package is installed.
var SystemWordLists = []string{"/usr/share/dict/words", "/usr/dict/words", "/usr/share/dict/web2", "/usr/share/dict/american-english"}

// Dictionary is a set of correctly spelled English words, lower case.
type Dictionary struct {
	// Path is the word list it was read from ("" when none was found).
	Path  string
	words map[string]bool
}

// NewDictionary builds a dictionary from words (tests, or a word list
// read elsewhere).
func NewDictionary(words ...string) *Dictionary {
	d := &Dictionary{words: make(map[string]bool, len(words))}
	for _, w := range words {
		d.add(w)
	}
	return d
}

func (d *Dictionary) add(w string) {
	w = strings.ToLower(strings.TrimSpace(w))
	if w != "" && !strings.HasPrefix(w, "#") {
		d.words[w] = true
	}
}

// Len is the number of words.
func (d *Dictionary) Len() int {
	if d == nil {
		return 0
	}
	return len(d.words)
}

// Empty reports a dictionary with no words: no system word list was found.
func (d *Dictionary) Empty() bool { return d.Len() == 0 }

// ReadDictionary reads a word list, one word per line.
func ReadDictionary(path string) (*Dictionary, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	d := &Dictionary{Path: path, words: make(map[string]bool, 240000)}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		d.add(sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

var (
	systemOnce sync.Once
	systemDict *Dictionary
)

// System returns the operating system's word list, read once: the first of
// SystemWordLists that exists. Without one it returns an empty dictionary;
// the check then leans on the project vocabulary, the known misspellings
// and the model pass.
func System() *Dictionary {
	systemOnce.Do(func() {
		for _, p := range SystemWordLists {
			d, err := ReadDictionary(p)
			if err != nil {
				continue
			}
			lg.WithField("path", p).WithField("words", d.Len()).Debug("read the system word list")
			systemDict = d
			return
		}
		lg.Debug("no system word list; spell check uses the project vocabulary, the known misspellings and the models")
		systemDict = NewDictionary()
	})
	return systemDict
}

// has reports whether w (lower case) is a word: as listed, or an
// inflection or prefixed form of a listed word, or two listed words run
// together.
func (d *Dictionary) has(w string) bool {
	if d.Len() == 0 {
		return false
	}
	if d.base(w) {
		return true
	}
	for _, p := range []string{"re", "un", "pre", "non", "sub", "multi", "de", "dis", "co", "over", "under", "inter", "auto", "mis"} {
		if len(w) > len(p)+3 && strings.HasPrefix(w, p) && d.base(w[len(p):]) {
			return true
		}
	}
	return d.compound(w)
}

// base reports whether w is listed, or a regular inflection of a listed
// word (plural, past tense, -ing, comparative, adverb).
func (d *Dictionary) base(w string) bool {
	if d.words[w] {
		return true
	}
	for _, s := range stems(w) {
		if d.words[s] {
			return true
		}
	}
	return false
}

// shortTails are the three-letter words a compound may end with
// ("timeout", "checksum"); any other second word needs four letters, so a
// typo rarely splits into two listed words ("invalidted" is not
// "invalid" + "ted").
var shortTails = map[string]bool{
	"out": true, "sum": true, "set": true, "end": true, "way": true, "map": true, "box": true, "log": true,
	"key": true, "bit": true, "tag": true, "row": true, "cap": true, "net": true, "web": true, "use": true,
	"off": true, "run": true, "top": true, "day": true, "man": true, "ups": true, "ins": true, "ons": true,
	"outs": true, "sums": true, "sets": true, "ends": true, "ways": true, "maps": true, "logs": true,
	"keys": true, "bits": true, "tags": true, "rows": true, "caps": true, "uses": true, "offs": true, "runs": true,
}

// compound reports whether w is two listed words (or an inflection of the
// second), the first at least 3 letters, the second at least 4 or one of
// shortTails, and the whole at least 7.
func (d *Dictionary) compound(w string) bool {
	if len(w) < 7 {
		return false
	}
	for i := 3; i <= len(w)-3; i++ {
		tail := w[i:]
		if !d.words[w[:i]] {
			continue
		}
		if shortTails[tail] || (len(tail) >= 4 && d.base(tail)) {
			return true
		}
	}
	return false
}

// stems returns the forms w may be an inflection of.
func stems(w string) []string {
	var out []string
	add := func(s string) {
		if len(s) >= 2 {
			out = append(out, s)
		}
	}
	undouble := func(s string) {
		if n := len(s); n >= 3 && s[n-1] == s[n-2] && !strings.ContainsRune("aeiou", rune(s[n-1])) {
			add(s[:n-1])
		}
	}
	switch {
	case strings.HasSuffix(w, "ies"):
		add(w[:len(w)-3] + "y")
		add(w[:len(w)-1])
	case strings.HasSuffix(w, "es"):
		add(w[:len(w)-2])
		add(w[:len(w)-1])
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		add(w[:len(w)-1])
	}
	switch {
	case strings.HasSuffix(w, "ied"):
		add(w[:len(w)-3] + "y")
		add(w[:len(w)-1])
	case strings.HasSuffix(w, "ed"):
		s := w[:len(w)-2]
		add(s)
		add(s + "e")
		undouble(s)
	}
	if strings.HasSuffix(w, "ing") {
		s := w[:len(w)-3]
		add(s)
		add(s + "e")
		undouble(s)
	}
	if strings.HasSuffix(w, "ings") {
		s := w[:len(w)-4]
		add(s)
		add(s + "e")
		undouble(s)
	}
	for _, suf := range []string{"er", "est", "ers"} {
		if strings.HasSuffix(w, suf) {
			s := w[:len(w)-len(suf)]
			add(s)
			add(s + "e")
			undouble(s)
			if strings.HasSuffix(s, "i") {
				add(s[:len(s)-1] + "y")
			}
		}
	}
	switch {
	case strings.HasSuffix(w, "ily"):
		add(w[:len(w)-3] + "y")
	case strings.HasSuffix(w, "ly"):
		add(w[:len(w)-2])
		add(w[:len(w)-2] + "le")
	}
	for _, suf := range []string{"ness", "ment", "ments", "able", "ability", "less", "ful", "ize", "izes", "ized", "izing", "ization"} {
		if strings.HasSuffix(w, suf) && len(w) > len(suf)+3 {
			s := w[:len(w)-len(suf)]
			add(s)
			add(s + "e")
			add(s + "y")
		}
	}
	return out
}

// known is the known-misspellings table: misspelling to correction, lower
// case, US spellings (golangci/misspell's main list, its UK-to-US list,
// and the main list's British corrections turned American).
var known = sync.OnceValue(func() map[string]string {
	us := make(map[string]string, len(misspell.DictAmerican)/2)
	for i := 0; i+1 < len(misspell.DictAmerican); i += 2 {
		us[misspell.DictAmerican[i]] = misspell.DictAmerican[i+1]
	}
	out := make(map[string]string, len(misspell.DictMain)/2+len(us))
	for i := 0; i+1 < len(misspell.DictMain); i += 2 {
		w, fix := misspell.DictMain[i], misspell.DictMain[i+1]
		if a, ok := us[fix]; ok {
			fix = a
		}
		out[w] = fix
	}
	for w, fix := range us {
		out[w] = fix
	}
	return out
})

// corrections is the set of every correction in the known table: words
// the table vouches for.
var corrections = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	for _, fix := range known() {
		out[fix] = true
	}
	return out
})

// Known returns the US correction of a known misspelling (lower case).
func Known(w string) (string, bool) {
	fix, ok := known()[strings.ToLower(w)]
	return fix, ok
}

// contractions are accepted as written; the word list has none.
var contractions = func() map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(`don't doesn't didn't isn't aren't wasn't weren't hasn't haven't hadn't won't
		wouldn't shouldn't couldn't can't cannot mustn't needn't shan't it's that's there's here's what's who's
		let's we're they're you're we've they've you've i'm we'll they'll you'll it'll that'll i've i'd we'd
		they'd you'd he's she's he'd she'd`) {
		out[w] = true
		out[strings.ReplaceAll(w, "'", "\u2019")] = true
	}
	return out
}()
