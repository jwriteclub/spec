# rws specification

Live relay of camera streams. Made-up fixture in the shape of a video-on-demand server: this file, `index`
and `hls` each number their sections from 1, and refer to each other with local, unqualified and
file-qualified `§` references and with `$` references.

## 1. Sessions

**Def $Rws.Session (session).** A session is one RTSP connection from the relay to one camera. Viewers
never open sessions themselves; they attach to an existing one (see §2).

**$Rws.OnePerCamera (one session per camera).** At most one $Rws.Session exists per camera at any instant,
however many viewers are attached. Checked by: a Go test that attaches 50 viewers to one camera and counts
sessions.

## 2. Relay

The relay copies frames from a session to every attached viewer and to the part packer described in §3.1.

**$Rws.FanOut (fan-out).** Every frame received on a $Rws.Session is delivered to every attached viewer in
arrival order, and a slow viewer is dropped rather than slowing the others. Checked by: a property test with
one stalled viewer.

### 2.1 Idle stop

**$Rws.IdleStop (idle stop).** A $Rws.Session with no attached viewer for 30 s is closed. Readers that want
the playlist side of this should see §hls:1, and the record side is in §index:2. Checked by: a fake-clock
test.

A closed session is gone for good, so a later viewer starts a new one, and $Rws.OnePerCamera still holds.
