// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package spell

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseWordsKeepsWordsAfterALongLine(t *testing.T) {
	long := strings.Repeat("x", 70*1024)
	got := ParseWords([]byte("alpha\r\n# " + long + "\nbeta\n" + long + "\ngamma"))
	assert.Equal(t, []string{"alpha", "beta", long, "gamma"}, got)
}
