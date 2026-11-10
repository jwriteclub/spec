// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwriteclub/spec/internal/recording"
)

const replayUsage = "replay DIR"

func (s *session) replayCommand() *cobra.Command {
	var out, video string
	var o recording.TapeOptions
	cmd := &cobra.Command{
		Use:   replayUsage,
		Short: "write a VHS tape that replays a recorded spec ui session",
		Long: "Writes a VHS tape (charmbracelet/vhs) that replays the session recorded in DIR (spec ui --record DIR):\n" +
			"it sets the terminal's size, starts `spec ui --replay DIR` hidden, waits for the first frame, then types\n" +
			"the recorded keys and pastes with the recorded pauses between them, unchanged. The replaying UI\n" +
			"feeds in the mouse events, other processes' file changes and model replies itself, after the same\n" +
			"keys. Keys VHS cannot press (Home, End, function keys, ...) are left out and listed on stderr.\n\n" +
			"The tape goes to DIR/replay.tape and records DIR/replay.mp4 unless -o and --video say otherwise;\n" +
			"render it with `vhs FILE`. The video is 3840x2160 (4K) at 30 frames per second, in the largest font at\n" +
			"which the recorded terminal surely fits; the replay lays the UI out at the recorded size and centres it\n" +
			"in the somewhat larger terminal VHS gives it. --width, --height, --font-size and --framerate override.",
		Args: needArgs(1, replayUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.Padding == 0 {
				o.Padding = -1 // TapeOptions: 0 is the default, negative is none
			}
			s.result = s.runReplay(args[0], out, video, o)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "write the tape to `FILE` (default DIR/replay.tape)")
	cmd.Flags().StringVar(&video, "video", "", "the video the tape records, `FILE` (default DIR/replay.mp4)")
	cmd.Flags().IntVar(&o.FontSize, "font-size", 0, "the tape's font size (default: the largest that fits)")
	cmd.Flags().IntVar(&o.Padding, "padding", recording.DefaultPadding, "the tape's padding in pixels")
	cmd.Flags().IntVar(&o.Width, "width", recording.DefaultWidth, "the video's width in pixels")
	cmd.Flags().IntVar(&o.Height, "height", recording.DefaultHeight, "the video's height in pixels")
	cmd.Flags().IntVar(&o.Framerate, "framerate", recording.DefaultFramerate, "the video's frames per second")
	return cmd
}

// replayData is the JSON data of spec replay.
type replayData struct {
	Tape     string   `json:"tape"`
	Video    string   `json:"video"`
	Warnings []string `json:"warnings"`
}

// runReplay writes the tape of the recording in dir.
func (s *session) runReplay(dir, out, video string, o recording.TapeOptions) *outcome {
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(s.env.Cwd, p)
	}
	dir, out, video = abs(dir), abs(out), abs(video)
	if a, err := filepath.Abs(dir); err == nil {
		dir = a
	}
	// The tape types DIR at a shell, where a control character is a key press.
	if recording.HasControl(dir) {
		return fail(exitFail, "usage", "the path of the recording holds a control character: %q", dir)
	}
	rec, err := recording.Load(dir)
	if err != nil {
		return fail(exitFail, "not_found", "cannot read the recording in %s: %v", dir, err)
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		o.Spec = exe
	}
	if video == "" {
		video = filepath.Join(dir, "replay.mp4")
	}
	if out == "" {
		out = filepath.Join(dir, "replay.tape")
	}
	// A recording is untrusted and may hold links: the tape replaces one, and
	// the video, which VHS writes later, must not be one.
	if st, err := os.Lstat(video); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fail(exitFail, "write_failed", "%s is a symbolic link, which the video would be written through; remove it or pass --video", video)
	}
	o.Video = video
	tape, warnings := recording.Tape(rec, o)
	if err := recording.WriteFileAtomic(out, []byte(tape)); err != nil {
		return fail(exitFail, "write_failed", "cannot write %s: %v", out, err)
	}
	var errText strings.Builder
	for _, w := range warnings {
		fmt.Fprintf(&errText, "spec replay: warning: %s\n", w)
	}
	return &outcome{
		data: replayData{Tape: out, Video: video, Warnings: append([]string{}, warnings...)},
		human: fmt.Sprintf("wrote %s; render it with: vhs %s\nthen %s lists what the replay did not reproduce (empty: nothing)\n",
			out, out, filepath.Join(dir, recording.ReplayLog)),
		stderr: errText.String(),
	}
}
