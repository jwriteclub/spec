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
