# lensd specification

What lensd guarantees, as definitions (`**Def $Name (title).**`) and invariants
(`**$Name (title).**`). Normative statements use MUST. Every invariant names what checks it: a TLA+
model in spec/tla/ (each with a deliberately broken "mutant" that TLC must reject), a Go test, or a
runtime check. Go tests cite what they check with a comment directly above the test:

    // Spec: $Frame.Timing §1 "quoted text from this file"

and code may cite the invariant it enforces (`// $Sdk.NvrAgrees: ...`). A spec change and the
matching test or model change go in the same commit. Values marked (code: …) also exist as
constants; changing one means changing the other. Liveness is stated as bounded-time rules on
lensd's own clock ($Time.Elapsed), not as TLA+ liveness properties. Trace verification (checking
runtime traces against the models) is out of scope.

## Context (for readers without the code)

lensd is a Go web gateway for two NVRs at one site. It relays live RTSP from the NVR (one
session per camera shared by all viewers), plays recordings through a vendor SDK helper process
(playhelper; each NVR serves about 4 playback sessions, and beyond that silently sends no data),
packs H.264 frames into in-memory fMP4 tracks served as LL-HLS (segments start on keyframes and are
split into ~0.5 s parts), runs ffmpeg "renditions" that transcode a source track to smaller sizes
(each ffmpeg start is a "generation"; a watchdog kills a stuck one), keeps a motion index built from
NVR alarm logs (tail every 30 s, backfill an hour at a time going back), and gates everything behind
SAML sign-in with 7-day session cookies.

## 0. Definitions

### 0.1 Time

**Def $Time.Media (media time).** Media time is an integer count of 90 kHz ticks (RTP timestamps on
the wire, fMP4 decode times in tracks). A duration is an integer number of ticks; "greater than
zero" means at least 1 tick; "(0, 5 s]" means 1..450000 ticks inclusive; "one frame at 15 fps" is
exactly 6000 ticks. RTP timestamps are 32-bit and wrap; the difference from one to the next is taken
as a signed 32-bit value, so it is correct across the wrap and a backwards step is negative (a
forward jump of more than 2^31 ticks, ~6.6 h, reads as backwards; both are handled the same way, see
$Frame.Timing). Track decode times are 64-bit (`tfdt` version 1) and never wrap.

**Def $Time.Wall (wall time).** Wall time is an absolute instant (Go `time.Time`), comparable across
zones, compared only with Before/After/Equal (never as strings or with `==`). Times an NVR reports
(alarm log, recording search, keyframe stamps inside recordings) are that NVR's local clock and are
converted with that NVR's UTC offset, which lensd learns from the NVR, before being stored or
compared. Assumption: the offset is fixed (the site has no daylight saving) and is learned once (see
Accepted behaviour).

**Def $Time.Elapsed (elapsed time).** Timeouts, graces and watchdogs are measured on lensd's own
monotonic clock (`time.Since` of a `time.Now()` value), never by subtracting wall times that came
from an NVR.

### 0.2 Intervals and nearness

**Def $Interval (interval).** An interval [Start, End] is closed, with Start <= End. Zero length
(Start = End) is valid: one instant.

**Def $Interval.Gap (gap, within).** For intervals A and B with A.Start <= B.Start, gap(A, B) =
B.Start - A.End, negative when they overlap. "Within d" means gap <= d; "more than d apart" means
gap > d. Every bound in this spec states whether it is inclusive; the default is inclusive at the
top ("at most N", "<= 5 s"). A fetch window [from, to) is half-open, so an event on a boundary
belongs to exactly one window.

## 1. Frames, segments and tracks

A Track is an in-memory HLS stream: an init segment, then segments of fMP4 fragments split into
parts. One goroutine writes a Track; HTTP handlers, renditions and the reaper read it. A seek in a
recording creates a new Track; nothing below relies on a track spanning a seek.

**$Frame.Timing (fragment timing).** Every fragment written to a Track, and so every fragment a
rendition's ffmpeg reads, MUST be one whole access unit (moof+mdat); NAL units sharing one RTP
timestamp are one access unit. A frame's duration is the time since the previous frame ($Time.Media)
and MUST be in (0, 5 s]; its decode time MUST equal the sum of all earlier durations in the track.
The first frame, and a frame whose delta is 0, negative, or > 5 s, gets 6000 ticks (one frame at 15
fps). Assumption: sources have no B-frames; a source with composition offsets MUST be logged and
refused, not silently mistimed. Checked by: property test over generated timestamp sequences (first
frame, equal, wrap, backwards,
> 5 s) reading durations back from the muxer's output bytes.

**$Frame.SegmentShape (segment shape).** Every published segment MUST start with a keyframe, contain
at least one part with at least one frame, have an Independent first part, and have a duration equal
to the sum of its parts. Out of memory: evicting old segments ($Hub.MemoryBound) MUST be tried
before abandoning the segment being written; a segment none of whose parts were published may be
abandoned and its number is reused by the next segment; once any part of a segment has been
published, the segment MUST be completed or the track MUST stop. Checked by: property test of track
shape after random writes, out-of-memory, eviction, stop and close; TLA+ TrackSegments Inv_Shape
(mutant abandoning a segment with published parts must fail). Known violated today (out of memory on
a segment's first frame leaves a zero-part segment and consumes its number).

**$Frame.Numbering (numbering).** Segment numbers MUST be contiguous and increasing, and the
playlist's MEDIA-SEQUENCE MUST equal the first kept segment's number; segments are only dropped from
the front. Checked by: same property test; TLA+ TrackSegments Inv_Numbering.

**$Frame.OneWriter (one writer).** WriteFrame called after the Track's Close or Stop has returned
MUST return ErrClosed and MUST NOT change the track; the live source MUST stop its sender before
Close. Checked by: test asserting ErrClosed and an unchanged track, plus the teardown race test
under -race.

**$Frame.CleanGeneration (each generation starts clean).** Each ffmpeg generation of a rendition
MUST be fed the init segment, then a part that begins a segment (keyframe), then parts in order with
no gap. A live reader that skips ahead (more than 4 s behind the live edge; code: liveMaxLag) or is
overtaken by eviction MUST start a new generation at a keyframe, never hand the running generation a
non-contiguous part. A stuck live generation (watchdog: output < 1% of input over 10 s; code:
stuckWindow) MUST be replaced by a new generation, not end the rendition. A replacement is not a new
request: it takes over its predecessor's transcoder slot, waits for the predecessor to exit, and
never gets 429. A generation that is stopped, or whose output has ended, MUST have exited within 1
s, else it is SIGKILLed (code: rendition exit timer, 5 s today); it holds its slot until it has
exited. If the rendition has no reader when a generation ends, the replacement may still start;
$Hub.IdleStop stops it. Checked by: rendition tests with a fake ffmpeg (one that ignores end of
input, one that ignores stop); TLA+ Transcoders Inv_ReplacementHoldsSlot, Inv_ReplacementNever429,
Inv_ExitBounded; TLA+ TrackSegments Inv_NoGap (mutant reader skipping after eviction must fail);
runtime: the stuck watchdog. Known violated today (a SIGKILLed generation ends the rendition; a skip
or eviction keeps feeding the same generation; the exit timer is 5 s).

**$Frame.Forward (time only moves forward).** Within a track, decode times MUST strictly increase
(follows from $Frame.Timing). PROGRAM-DATE-TIME (wall time ($Time.Wall) from NVR keyframe stamps;
for live tracks, lensd's clock at the first frame plus media time) MUST NOT go backwards between
consecutive segments (equal is allowed); a recording gap moves it forward. If an NVR keyframe stamp
goes backwards, lensd MUST clamp, log, and put EXT-X-DISCONTINUITY before that segment. Checked by:
muxer and keyframe-stamp tests across a 32-bit wrap and a backwards NVR clock.

**$Frame.InitFixed (init never changes).** A published init segment MUST NOT change. A change of
SPS/PPS (resolution, profile) MUST stop the track; the next request starts a new one. Checked by:
test feeding a mid-stream SPS change.

## 2. HLS hub (transcoders, idle, memory)

**$Hub.TranscoderCap (transcoder cap).** Running rendition ffmpegs (started and not yet exited) MUST
be at most max_transcoders; the check and the reservation MUST be one atomic step, and a request
that would exceed the cap MUST get 429 (the browser falls back to a smaller rendition or the
substream). Source tracks do not count. A generation still exiting counts (bounded by
$Frame.CleanGeneration's 1 s), so a new request may briefly get 429; a replacement generation never
does. Checked by: concurrent transcoder cap test; TLA+ Transcoders Inv_Cap (mutant with a non-atomic
check and reserve must fail).

**$Hub.TakenOver (taken over).** A recording track is taken over when its lease is preempted by
another lensd viewer ($Sdk.Preempt). From the instant lensd detects it ($Time.Elapsed), requests for
the track MUST get 409 while elapsed <= 30 s and MUST NOT create a lease; the first request after 30
s retries (code: takenGrace). The preempted viewer is not a waiting viewer for $Sdk.Preempt. Checked
by: taken-grace test.

**$Hub.IdleStop (idle tracks stop).** A track with no reader MUST be stopped within its idle limit
plus 5 s: 30 s for live, 90 s for recordings (code: liveIdleTimeout, recIdleTimeout, 5 s reaper
tick), releasing its transcoders and lease. Checked by: reaper test with a fake clock.

**$Hub.BlockingAnswer (blocking requests answer).** A blocking LL-HLS request
(`_HLS_msn`/`_HLS_part`) MUST be answered when the part is published, when the track stops (with an
error), or after 20 s, whichever is first. Checked by: test stopping a track while a request waits.

**$Hub.MemoryBound (memory bound).** All tracks share one fixed-size arena; kept segment bytes MUST
never exceed it. When it is full, the oldest finished segment of the least recently used track,
never a track's only kept segment (code: EvictOldest), MUST be evicted before a segment is
abandoned. Checked by: the $Frame.SegmentShape property test on a tiny arena; TLA+ TrackSegments
Inv_ArenaBound, Inv_KeepsOne.

## 3. Live relay

**$Relay.OneSession (one session per camera).** At any instant there MUST be at most one open RTSP
session to the NVR per stream name, where a session is open from SETUP sent until its socket is
closed (so an old socket during a reconnect counts). Checked by: TLA+ RelayHub Inv_OneSession;
concurrent add/remove storm test. Known violated today (the reconnect worker dials the new session
before closing the old one).

**$Relay.NoOrphans (no orphans).** A stream that has a consumer MUST be the hub's current entry for
its name. A stream whose last consumer has left MUST have started teardown, and its socket MUST be
closed within 5 s. Checked by: TLA+ RelayHub Inv_NoOrphan (mutant without the dead-stream check,
today's code, must fail) and Inv_IdleNoSession (mutant without the worker's generation check must
fail); storm test. Known violated today (a join racing the last leave can orphan a live stream).

**$Relay.Reconnect (reconnect).** A stream with consumers MUST have an open session or be
reconnecting, with backoff 1 s for 5 tries, then 5 s for 5, then 30 s (code: backoff), for as long
as it has consumers; a new session MUST open only after the old socket is closed, and AddConsumer
MUST NOT dial while the worker is reconnecting. Checked by: TLA+ RelayHub Inv_Reconnecting,
Inv_OneSession; test with a fake dial that fails and recovers. Known violated today (dial before
close, as $Relay.OneSession).

## 4. Recorder sessions (leases)

**$Sdk.LeaseCap (leases fit the cap).** Per NVR, leases held, including preempted but not yet
released, and quarantined slots ($Sdk.NvrAgrees), MUST be at most sdk_sessions_per_nvr (default 4).
Checked by: TLA+ SdkLease Inv_LeasesLeqCap; sdk tests.

**$Sdk.NvrAgrees (the NVR agrees).** The NVR MUST never see more than sdk_sessions_per_nvr playbacks
from lensd. A holder requests the stop by closing its playhelper socket; its slot MUST stay counted
until playhelper has stopped the NVR playback for that connection. If that is not confirmed within 5
s, the holder is let go (its Release returns) but the slot stays counted as quarantined, logged as a
suspected stuck NVR session; if a slot is still quarantined 30 s later, lensd MUST restart playhelper
(ending its NVR sessions; their slots are freed once it has exited) and log it. Assumption: a
playhelper exit ends its NVR playbacks. Checked by: TLA+ SdkLease Inv_NvrSessionsLeqCap (mutant
releasing early, today's code, must fail); fake-playhelper tests, including one that never confirms.
Known violated today (released on socket close, before the helper stops).

**$Sdk.ViewersFirst (viewers beat analysis).** No step may preempt on behalf of analysis, and no
step may grant an analysis Acquire while a viewer is waiting on that NVR. Checked by: TLA+ SdkLease
Inv_ViewersBeatAnalysis (history flag; mutant granting analysis over a waiting viewer must fail);
sdk tests.

**$Sdk.Preempt (preempt exactly as needed).** At each step where a viewer finds the NVR full, it
MUST preempt if and only if preempted-but-unreleased leases are fewer than waiting viewers, taking
the newest analysis lease, else the oldest viewer lease. A preempted lease may outlive the waiter
that caused it (that waiter may get another freed slot or give up, $Sdk.BoundedWait). Ordering among
more waiters than slots is best-effort. Checked by: TLA+ SdkLease Inv_PreemptAsNeeded,
Inv_PreemptOrder, Inv_PreemptedNotWaiter; sdk tests.

**$Sdk.BoundedWait (bounded waits).** A viewer's Acquire MUST succeed or fail within 15 s (code:
sessionWait); an analysis Acquire MUST give up within 20 s (code: acquireWait). Checked by: sdk
tests with a full NVR.

**$Sdk.NoSilentPlayer (no silent black players).** A recording lease that has received no frame
within 10 s of Play MUST fail its request, be released, and be logged (covers a full NVR, slots held
by another client, sessions left from a lensd crash). Checked by: fake-playhelper test that sends
nothing.

## 5. Motion index

lensd keeps, per camera, a list of motion intervals built from NVR alarm-log events, plus per-NVR
watermarks: Tail (complete going forward up to here) and Backfill (complete going back to here; it
advances by moving earlier). Both persist in one file.

**$Motion.Watermarks (watermarks are honest).** A watermark MUST advance over a window [from, to)
only after every page the NVR returned for that window was received and merged into the index. Each
tail fetch starts 1 min before Tail (the NVR writes some entries late; code: motion.go tail
overlap). Tail MUST NOT pass lensd's own clock. Index and watermarks MUST be saved together
atomically (temp file + rename); a missing or unreadable file MUST reset the watermarks and start an
empty index. Checked by: TLA+ MotionIndex Inv_Honest (mutant advancing on cancel, today's code, must
fail), Inv_TailClock; test cancelling mid-pagination; save/load tests including a corrupt file.
Known violated today (cancel mid-fetch advances the watermark).

**$Motion.Merged (intervals are merged).**
- Step: two intervals within 10 s of each other ($Interval.Gap, gap <= 10 s; code: mergeGap) merge
  into one interval spanning both.
- A list is merged when it is sorted by Start, every interval satisfies $Interval, and no step
  applies (all gaps > 10 s).
- Cutoff = now - retention. Discard threshold = cutoff - 10 s: raw events ending before it MUST be
  discarded on arrival, and stored intervals ending before it may be dropped at any time. No other
  discard is allowed (in particular, nothing is dropped at the cutoff itself).
- The view at the cutoff: the index with intervals ending before the cutoff removed and starts
  clamped to the cutoff. Queries MUST return only this view.
- The stored per-camera index MUST always be merged. For a fixed cutoff, merging batches into it, in
  any order, with discards at any points, MUST give the same view at the cutoff as merging all raw
  events at once. Merging the same batch twice MUST equal merging it once (tail and backfill windows
  overlap, so duplicates are normal). Before the cutoff the index is not exact: the oldest interval
  may start later than a one-shot merge would say.
- Coverage (at or after the cutoff): every raw event MUST be covered by an interval, and the index
  MUST cover only raw events plus gaps of at most 10 s between them. Checked by: TLA+ MotionIndex
  Inv_Merged, Inv_OrderIndependent, Inv_Idempotent, Inv_Covered, Inv_CoversOnly, Inv_NoOtherDiscard,
  Inv_MergeAgree (Go-style fold equals the step rule; mutants discarding at the cutoff, on arrival
  or from the index, must fail Inv_NoOtherDiscard); property test (random events, random batches
  with duplicates, random order, discards at random points, vs one merge, against a naive
  apply-steps-until-none-apply reference). Known violated today (queries return intervals ending
  before the cutoff).

## 6. Sign-in

**$Auth.Gated (everything is gated).** With auth enabled, every request except to /saml/acs,
/saml/metadata and /saml/logout MUST be refused without an allowed session. An allowed session is a
session cookie issued less than 7 days ago (session_lifetime) for which allow() ($Auth.OneDecision)
is true. A page load is a GET to a path outside /api/, /hls/, /snap/ and /analysis/; without an
allowed session it is redirected to the identity provider, and every other request gets 401. Note: lensd's routes
accept GET only (and the HEAD Go derives from it). Adding any other method to a route requires
amending this rule; the GET-only test fails until it is. Checked by: test walking the real route
table plus a path matching no route; TestRoutesGETOnly.

**$Auth.OneDecision (one decision).** allow(email, groups, cfg) is one function: denied_emails
first, then allowed emails, domains or groups. Sign-in and every request MUST call it, with the
email and groups stored in the session (stored under the configured attribute name, whichever name
matched at sign-in) and the current config. Checked by: deny-after-sign-in and group-attribute-name
tests.

## Accepted behaviour (by design; not bugs)

- **Viewer rotation.** With more recording viewers on one NVR than slots (e.g. 5 on 4), each
  newcomer takes the oldest viewer's slot and that viewer waits out its 30 s grace ($Hub.TakenOver),
  so playback rotates. Fine for this site.
- **Analysis can starve.** While viewers keep an NVR full, analysis waits ($Sdk.ViewersFirst) and
  catches up later.
- **Revocation lag.** Removing someone from the allowed group takes effect when their cookie expires
  (up to 7 days); denied_emails is the immediate lever (next request). Changing the SP key signs
  everyone out. A request already in progress when someone is denied finishes (at most 20 s,
  $Hub.BlockingAnswer).
- **NVR time zone is learned once.** If an NVR's time zone were changed, stored times would be off
  until lensd restarts. The site has no daylight saving.
- **Clamped PDT.** During a clamp ($Frame.Forward) the player's clock may hold still while video
  plays, marked by a discontinuity.
