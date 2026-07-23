package render

import "testing"

func TestSanitizeANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no escapes passes through", "plain text line", "plain text line"},
		{"empty stays empty", "", ""},
		{
			"SGR colour preserved verbatim",
			"a \x1b[32mgreen\x1b[0m b",
			"a \x1b[32mgreen\x1b[0m b",
		},
		{
			"SGR with multiple params preserved",
			"\x1b[1;31;40mbold red on black\x1b[0m",
			"\x1b[1;31;40mbold red on black\x1b[0m",
		},
		{
			"cursor-up CSI dropped, text kept",
			"before\x1b[2Aafter",
			"beforeafter",
		},
		{
			"erase-line CSI dropped",
			"\x1b[2Kcleared",
			"cleared",
		},
		{
			"cursor-position CSI dropped",
			"\x1b[10;20Hmoved",
			"moved",
		},
		{
			"private-mode set (alt screen) dropped",
			"\x1b[?1049hshown",
			"shown",
		},
		{
			"OSC title string dropped (BEL terminated)",
			"\x1b]0;window title\x07visible",
			"visible",
		},
		{
			"OSC string dropped (ST terminated)",
			"\x1b]8;;http://x\x1b\\link",
			"link",
		},
		{
			"charset designator (ESC ( B) fully dropped",
			"\x1b(Btext",
			"text",
		},
		{
			"broken/partial CSI (no final byte) dropped whole",
			"good\x1b[38;5;",
			"good",
		},
		{
			"lone trailing ESC dropped",
			"tail\x1b",
			"tail",
		},
		{
			"broken OSC (no terminator) dropped whole",
			"start\x1b]0;never ends",
			"start",
		},
		{
			"SGR kept while adjacent cursor CSI dropped",
			"\x1b[31m\x1b[2Kred\x1b[0m",
			"\x1b[31mred\x1b[0m",
		},
		{
			"CSI broken by stray byte drops through terminator",
			"x\x1b[3\x00y",
			"xy",
		},
		{
			// A C0 control byte before a would-be SGR final breaks the
			// sequence: the broken \x1b[1\x00 is dropped and the trailing
			// 'm' survives as literal text (never as a kept SGR).
			"CSI broken before SGR final: m survives as literal",
			"\x1b[1\x00m",
			"m",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeANSI(tc.in); got != tc.want {
				t.Errorf("sanitizeANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResetSeq(t *testing.T) {
	// The reset appended per rendered line must be exactly SGR 0 so styles
	// never bleed between lines.
	if resetSeq != "\x1b[0m" {
		t.Errorf("resetSeq = %q, want %q", resetSeq, "\x1b[0m")
	}
}
