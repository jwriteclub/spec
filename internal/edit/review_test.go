// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rename is byte-exact around a BOM, CRLF line endings, a setext
// section-form label, a title and non-ASCII text, and writes exactly that.
func TestRenameByteExactBOMAndCRLF(t *testing.T) {
	src := "\xef\xbb\xbf$Ab.Cd: tïtle $Ab.Cd\r\n---\r\nBody 😀$Ab.Cd, $Ab.Cd's, $Ab.Cd.Ef, `$Ab.Cd`.\r\n\r\n**$Ef.Gh (on $Ab.Cd).** x $Ab.Cd\r\n"
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.spec.md"), []byte(src), 0o640))
	spec := buildSpec(map[string]string{"a.spec.md": src})
	res, err := PlanRename(dir, spec, "Ab.Cd", "Xy.Zw")
	require.NoError(t, err)
	want := strings.NewReplacer("$Ab.Cd: ", "$Xy.Zw: ", "tle $Ab.Cd\r", "tle $Xy.Zw\r", "😀$Ab.Cd,", "😀$Xy.Zw,",
		" $Ab.Cd's", " $Xy.Zw's", "on $Ab.Cd)", "on $Xy.Zw)", "x $Ab.Cd\r", "x $Xy.Zw\r").Replace(src)
	assert.Equal(t, 6, res.Tokens())
	require.Len(t, res.LeftAlone, 1)
	require.NoError(t, res.Plan.Commit())
	got, err := os.ReadFile(filepath.Join(dir, "a.spec.md"))
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
	st, err := os.Stat(filepath.Join(dir, "a.spec.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), st.Mode().Perm())
	left, err := filepath.Glob(filepath.Join(dir, ".spec-edit-*"))
	require.NoError(t, err)
	assert.Empty(t, left)
}
