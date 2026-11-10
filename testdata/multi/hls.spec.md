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
