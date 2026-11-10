<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# lensd name table

`lensd.spec.md` is the spec of lensd, a made-up camera gateway, used as a test fixture. It was
first written with numbered labels and then converted to logedit's markup: each numbered label became
a named one (`**INV-FRAME-1 (fragment timing).**` became `**$Frame.Timing (fragment timing).**`, and
`**DEF-TIME-1 (media time).**` became `**Def $Time.Media (media time).**`), and every citation of an
old id became the new `$Name` (including those inside code, which stay opaque).

| Old id | New label | Title |
|--------|-----------|-------|
| `DEF-TIME-1` | `Def $Time.Media` | media time |
| `DEF-TIME-2` | `Def $Time.Wall` | wall time |
| `DEF-TIME-3` | `Def $Time.Elapsed` | elapsed time |
| `DEF-IVL-1` | `Def $Interval` | interval |
| `DEF-IVL-2` | `Def $Interval.Gap` | gap, within |
| `INV-FRAME-1` | `$Frame.Timing` | fragment timing |
| `INV-FRAME-2` | `$Frame.SegmentShape` | segment shape |
| `INV-FRAME-3` | `$Frame.Numbering` | numbering |
| `INV-FRAME-4` | `$Frame.OneWriter` | one writer |
| `INV-FRAME-5` | `$Frame.CleanGeneration` | each generation starts clean |
| `INV-FRAME-6` | `$Frame.Forward` | time only moves forward |
| `INV-FRAME-7` | `$Frame.InitFixed` | init never changes |
| `INV-HUB-1` | `$Hub.TranscoderCap` | transcoder cap |
| `INV-HUB-2` | `$Hub.TakenOver` | taken over |
| `INV-HUB-3` | `$Hub.IdleStop` | idle tracks stop |
| `INV-HUB-4` | `$Hub.BlockingAnswer` | blocking requests answer |
| `INV-HUB-5` | `$Hub.MemoryBound` | memory bound |
| `INV-RELAY-1` | `$Relay.OneSession` | one session per camera |
| `INV-RELAY-2` | `$Relay.NoOrphans` | no orphans |
| `INV-RELAY-3` | `$Relay.Reconnect` | reconnect |
| `INV-SDK-1` | `$Sdk.LeaseCap` | leases fit the cap |
| `INV-SDK-2` | `$Sdk.NvrAgrees` | the NVR agrees |
| `INV-SDK-3` | `$Sdk.ViewersFirst` | viewers beat analysis |
| `INV-SDK-4` | `$Sdk.Preempt` | preempt exactly as needed |
| `INV-SDK-5` | `$Sdk.BoundedWait` | bounded waits |
| `INV-SDK-6` | `$Sdk.NoSilentPlayer` | no silent black players |
| `INV-MOTION-1` | `$Motion.Watermarks` | watermarks are honest |
| `INV-MOTION-2` | `$Motion.Merged` | intervals are merged |
| `INV-AUTH-1` | `$Auth.Gated` | everything is gated |
| `INV-AUTH-2` | `$Auth.OneDecision` | one decision |
