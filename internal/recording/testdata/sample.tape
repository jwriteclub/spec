# spec replay of /rec/demo (recorded 2026-11-01T14:02:03Z, 140x40); timings verbatim
Output "/rec/demo/replay.mp4"
Set Shell "bash"
Set FontSize 34
Set LineHeight 1.2
Set Padding 10
Set Width 3840
Set Height 2160
Set Framerate 30
Set TypingSpeed 0ms
Hide
Type "exec '/usr/local/bin/spec' ui --replay '/rec/demo' || exit"
Enter
Wait+Screen@30s /\^G/
Show
Sleep 1830ms
Ctrl+O
Sleep 412ms
Type "x"
Sleep 148ms
# unsupported key "home" (#3)
# feeder: hover editor $Time.Media (after key #3, +200ms)
# feeder: watcher other.spec.md (after key #3, +600ms)
Sleep 1100ms
Type 'say "hi"'
Enter
Type "ok"
# resize to 120x40 (not replayed)
Sleep 600ms
Enter
Sleep 900ms
Hide
Sleep 1000ms
