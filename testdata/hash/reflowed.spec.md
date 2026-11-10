# Hash vector

**Def $Time.Elapsed (elapsed time).** Timeouts,
  graces   and watchdogs are measured on lensd's own
  monotonic   clock (`time.Since` of a `time.Now()`
  value),   never by subtracting wall times that
  came   from an NVR.
