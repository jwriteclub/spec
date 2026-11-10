// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package proposal

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitStaleMarkerWakesExistingAndLateBatchWaiters(t *testing.T) {
	st := NewStore(t.TempDir())
	p, q := &Proposal{Kind: KindChange}, &Proposal{Kind: KindAnalysis}
	require.NoError(t, st.Create(p))
	require.NoError(t, st.Create(q))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{})
	var once sync.Once
	type answer struct {
		ps  []*Proposal
		err error
	}
	done := make(chan answer, 1)
	go func() {
		ps, err := st.WaitObserved(ctx, []string{p.ID, q.ID}, time.Hour, func([]*Proposal) error { once.Do(func() { close(ready) }); return nil })
		done <- answer{ps, err}
	}()
	<-ready
	p.StalePrimary = &StalePrimary{Reason: "$A changed; inspect and refile"}
	require.NoError(t, st.Save(p))
	got := <-done
	require.NoError(t, got.err)
	require.Len(t, got.ps, 2)
	assert.NotNil(t, got.ps[0].StalePrimary)
	assert.Equal(t, StatusPending, got.ps[1].Status)
	late, err := st.Wait(ctx, []string{p.ID, q.ID}, time.Hour)
	require.NoError(t, err)
	require.Len(t, late, 2)
	assert.False(t, late[0].Decided())
	assert.Nil(t, late[0].Decision)
	st.Now = func() time.Time { return time.Now().Add(ReceiptTTL * 2) }
	all, err := st.List()
	require.NoError(t, err)
	assert.Len(t, all, 2, "pending stale receipts never expire")
}
