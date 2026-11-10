// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package recording

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSameFrame: stage times may differ; anything else may not.
func TestSameFrame(t *testing.T) {
	rec := "triage cf/glm-5.3-flash 0.0 s: pass\ncheck cf/glm-5.3 12.4 s: 1 finding"
	assert.True(t, SameFrame(rec, "triage cf/glm-5.3-flash 0.1 s: pass\ncheck cf/glm-5.3 9.8 s: 1 finding"))
	assert.False(t, SameFrame(rec, "triage cf/glm-5.3-flash 0.1 s: fail\ncheck cf/glm-5.3 9.8 s: 1 finding"))
	assert.False(t, SameFrame("line 5.3", "line 5.4"))
}
