// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// An omitted finish must still join the real rendering program before
// resource cleanup touches App fields. The cancellation callback observes
// Run completion directly, rather than hoping the race detector catches it.
func TestFlowCleanupJoinsProgramWithoutFinish(t *testing.T) {
	var f *flow
	var watcher *propWatcher
	cancelledAfterRun := make(chan bool, 1)
	t.Run("omitted finish", func(t *testing.T) {
		opts := testOptions(lensdProject(t))
		opts.WatchProposals = true
		f = startFlow(t, opts)
		f.do(func(a *App) {
			watcher = a.propW
			a.job = &job{cancel: func() {
				select {
				case <-f.done:
					cancelledAfterRun <- true
				default:
					cancelledAfterRun <- false
				}
			}}
		})
		require.NotNil(t, watcher)
		require.NotEmpty(t, f.frame())
		// Deliberately omit finish: t.Cleanup owns shutdown.
	})
	select {
	case ordered := <-cancelledAfterRun:
		require.True(t, ordered, "App.Close must follow Program.Run")
	case <-time.After(DriverTimeout):
		t.Fatal("App.Close did not cancel background work")
	}
	requireClosed(t, f.done, "Program.Run")
	requireClosed(t, watcher.done, "proposal watcher")
	require.Nil(t, f.a.propW)
	require.Nil(t, f.a.job)
	f.close() // repeated cleanup must not block or cancel again
	require.Empty(t, cancelledAfterRun)
}

// Kill returns before the event loop has exited. Keep that interval under
// explicit test control so premature resource cleanup fails deterministically.
type delayedShutdownProgram struct {
	done      chan struct{}
	killed    chan struct{}
	quitCalls atomic.Int32
	killCalls atomic.Int32
}

func (*delayedShutdownProgram) Send(tea.Msg) {}
func (p *delayedShutdownProgram) Quit() {
	p.quitCalls.Add(1)
	<-p.done
}
func (p *delayedShutdownProgram) Kill() {
	p.killCalls.Add(1)
	close(p.killed)
}

func TestDriverForcedCloseJoinsBeforeCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &delayedShutdownProgram{done: make(chan struct{}), killed: make(chan struct{})}
		runReturned := sync.OnceFunc(func() { close(p.done) })
		defer runReturned() // also release the fake event loop if an assertion fails
		cancelled := make(chan bool, 1)
		d := &Driver{p: p, done: p.done, a: &App{job: &job{cancel: func() {
			select {
			case <-p.done:
				cancelled <- true
			default:
				cancelled <- false
			}
		}}}}
		returned := make(chan struct{})
		go func() {
			d.close(0) // force cancellation without relying on scheduler timing
			close(returned)
		}()
		awaitClosed(t, p.killed, "forced cancellation")
		// Wait until the closer is blocked on Run completion. A premature
		// cleanup/return has then definitely happened, regardless of scheduling.
		synctest.Wait()
		select {
		case <-cancelled:
			t.Error("App resources closed before Program.Run returned")
		default:
		}
		select {
		case <-returned:
			t.Error("Driver.Close returned before Program.Run returned")
		default:
		}
		runReturned()
		awaitClosed(t, returned, "Driver.Close")
		require.Len(t, cancelled, 1)
		require.True(t, <-cancelled, "resource cancellation must observe Run completion")
		require.EqualValues(t, 1, p.killCalls.Load())
		require.EqualValues(t, 1, p.quitCalls.Load())
		d.Close()
		require.Empty(t, cancelled, "idempotent Close must not release resources twice")
		require.EqualValues(t, 1, p.quitCalls.Load())
	})
}

func TestDriverCloseJoinsRealProgramAndWatchers(t *testing.T) {
	opts := testOptions(lensdProject(t))
	opts.Watch, opts.WatchProposals = true, true
	d, err := NewDriver(opts, frameW, frameH)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	var files *watcher
	var proposals *propWatcher
	require.NoError(t, d.do(func(a *App) { files, proposals = a.watcher, a.propW }))
	require.NotNil(t, files)
	require.NotNil(t, proposals)
	d.Close()
	requireClosed(t, d.done, "Program.Run")
	requireClosed(t, files.done, "file watcher")
	requireClosed(t, proposals.done, "proposal watcher")
	require.NoError(t, d.runErr)
	d.Close()
	_, err = d.Frame()
	require.ErrorContains(t, err, "program ended")
}

// Fatal/require failures use Goexit. It must unwind and join the program,
// wake the waiting probe with an error, and still permit normal resource cleanup.
func TestDriverCloseAfterProbeGoexit(t *testing.T) {
	opts := testOptions(lensdProject(t))
	opts.Watch, opts.WatchProposals = true, true
	d, err := NewDriver(opts, frameW, frameH)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	var files *watcher
	var proposals *propWatcher
	require.NoError(t, d.do(func(a *App) { files, proposals = a.watcher, a.propW }))
	err = d.do(func(*App) { runtime.Goexit() })
	require.ErrorContains(t, err, "program exited without returning")
	d.Close()
	requireClosed(t, d.done, "aborted Program.Run")
	requireClosed(t, files.done, "file watcher")
	requireClosed(t, proposals.done, "proposal watcher")
	d.Close()
}

func requireClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatalf("%s is still running", what)
	}
}

func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(DriverTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}
