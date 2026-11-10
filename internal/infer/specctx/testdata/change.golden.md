## Proposed change

action: change
anchor: $Rws.IdleStop
file: rws.spec.md
kind: inv
reason: viewers reconnect slowly on mobile

### Current text

<text>
**$Rws.IdleStop (idle stop).** A $Rws.Session with no attached viewer for 30 s is closed. Readers that want
the playlist side of this should see §hls:1, and the record side is in §index:2. Checked by: a fake-clock
test.
</text>

### Proposed text

<text>
**$Rws.IdleStop (idle stop).** A $Rws.Session with no attached viewer for 60 s is closed. Readers that want
the playlist side of this should see §hls:1, and the record side is in §index:2. Checked by: a fake-clock
test.
</text>

## Related anchors (two hops of $Name references, either direction)

### $Rws.Session (def, rws.spec.md:9, 1 hop)

<text>
**Def $Rws.Session (session).** A session is one RTSP connection from the relay to one camera. Viewers
never open sessions themselves; they attach to an existing one (see §2).
</text>

### $Hls.Part (def, hls.spec.md:7, 2 hops)

<text>
**Def $Hls.Part (part).** A part is a slice of a segment of about half a second, always starting on a
frame boundary. Parts are built from frames that the relay delivers from a $Rws.Session.
</text>

### $Index.Record (def, index.spec.md:7, 2 hops)

<text>
**Def $Index.Record (record).** A record is one motion interval for one camera: a start and an end, both on
the server's monotonic clock, with start <= end. Records come from alarm-log lines; the relay's
$Rws.Session objects play no part in producing them.
</text>

### $Rws.OnePerCamera (inv, rws.spec.md:12, 2 hops)

<text>
**$Rws.OnePerCamera (one session per camera).** At most one $Rws.Session exists per camera at any instant,
however many viewers are attached. Checked by: a Go test that attaches 50 viewers to one camera and counts
sessions.
</text>

### $Rws.FanOut (inv, rws.spec.md:20, 2 hops)

<text>
**$Rws.FanOut (fan-out).** Every frame received on a $Rws.Session is delivered to every attached viewer in
arrival order, and a slow viewer is dropped rather than slowing the others. Checked by: a property test with
one stalled viewer.
</text>

## Anchor index

- $Hls.Part (def, hls.spec.md): A part is a slice of a segment of about half a second, always starting on a frame boundary.
- $Hls.KeyframeStart (inv, hls.spec.md): Every segment starts with a keyframe, so a viewer can join at any segment boundary.
- $Hls.PartDuration (inv, hls.spec.md): A $Hls.Part lasts at most 600 ms, and a part boundary never falls inside a frame.
- $Hls.Generation (def, hls.spec.md): A generation is one ffmpeg process lifetime for one rendition.
- $Hls.WatchdogKills (inv, hls.spec.md): The watchdog kills a $Hls.Generation that has produced no part for 10 s, and then lets the replacement start.
- $Index.Record (def, index.spec.md): A record is one motion interval for one camera: a start and an end, both on the server's monotonic clock, with start <= end.
- $Index.Monotonic (inv, index.spec.md): For one camera, records are stored in increasing order of start, and no two overlap.
- $Index.TailInterval (inv, index.spec.md): The tail reads new alarm-log lines every 30 s, and a $Index.Record is never reported before the tail has read the line that ends it.
- $Index.Backfill (inv, index.spec.md): Backfill walks backwards one hour at a time from the oldest known $Index.Record, and stops when an hour yields no log lines.
- $Rws.Session (def, rws.spec.md): A session is one RTSP connection from the relay to one camera.
- $Rws.OnePerCamera (inv, rws.spec.md): At most one $Rws.Session exists per camera at any instant, however many viewers are attached.
- $Rws.FanOut (inv, rws.spec.md): Every frame received on a $Rws.Session is delivered to every attached viewer in arrival order, and a slow viewer is dropped rather than slowing the others.
- $Rws.IdleStop (inv, rws.spec.md): A $Rws.Session with no attached viewer for 30 s is closed.

## Files

<file path="hls.spec.md">
# hls specification

LL-HLS playlists and renditions. Made-up fixture; see `rws` for sessions and `index` for records.

## 1. Playlists

**Def $Hls.Part (part).** A part is a slice of a segment of about half a second, always starting on a
frame boundary. Parts are built from frames that the relay delivers from a $Rws.Session.

**$Hls.KeyframeStart (segments start on keyframes).** Every segment starts with a keyframe, so a viewer can
join at any segment boundary. Checked by: a Go test over generated GOP structures. Parts are described in
§2 and renditions in §3.

## 2. Parts

**$Hls.PartDuration (part duration).** A $Hls.Part lasts at most 600 ms, and a part boundary never falls
inside a frame. Checked by: a property test over random frame durations.

## 3. Renditions

An ffmpeg rendition is restarted by a watchdog when it stalls; each start is a generation (§3.1).

### 3.1 Generations

**Def $Hls.Generation (generation).** A generation is one ffmpeg process lifetime for one rendition. At
most one generation per rendition is running, and the replacement may start only when the previous one has
exited. Records of when this happened are kept as $Index.Record entries for the camera, see §index:1.

**$Hls.WatchdogKills (watchdog kills).** The watchdog kills a $Hls.Generation that has produced no part for
10 s, and then lets the replacement start. Checked by: a fake-clock test. Also see §rws:2.1.
</file>
<file path="index.spec.md">
# index specification

The motion and recording index, built from NVR alarm logs. Made-up fixture; see `rws` for sessions.

## 1. Records

**Def $Index.Record (record).** A record is one motion interval for one camera: a start and an end, both on
the server's monotonic clock, with start <= end. Records come from alarm-log lines; the relay's
$Rws.Session objects play no part in producing them.

**$Index.Monotonic (monotonic records).** For one camera, records are stored in increasing order of start,
and no two overlap. Checked by: a property test that shuffles alarm-log lines before ingesting them.

## 2. Tail and backfill

The index is kept current by tailing the log and filled in by backfilling. A record becomes visible to
playlists (§hls:1) only after the tail has seen its end.

**$Index.TailInterval (tail interval).** The tail reads new alarm-log lines every 30 s, and a $Index.Record
is never reported before the tail has read the line that ends it. Checked by: a fake-clock test.

**$Index.Backfill (backfill).** Backfill walks backwards one hour at a time from the oldest known
$Index.Record, and stops when an hour yields no log lines. Checked by: a test with a synthetic log. Frame
timing for backfilled parts is described in §3.1.
</file>
<file path="rws.spec.md">
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
</file>
