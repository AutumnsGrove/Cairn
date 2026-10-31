package domain

import "testing"

func TestFormatCues(t *testing.T) {
	cases := map[string]string{
		"watched it at the cinema":  FormatCinema,
		"saw it on the big screen":  FormatCinema,
		"finished it on netflix":    FormatStreaming,
		"listened to the audiobook": FormatAudiobook,
		"read the hardback":         FormatHardback,
		"read the paperback":        FormatPaperback,
		"read it on my kindle":      FormatEbook,
		"watched the blu-ray":       FormatPhysical,
		"saw them live":             FormatLive,
		"caught it on tv":           FormatBroadcast,
	}
	for text, want := range cases {
		got := FormatCues(text)
		if len(got) == 0 || got[0] != want {
			t.Errorf("FormatCues(%q) = %v, want %q first", text, got, want)
		}
	}
}

func TestFormatCuesMultiple(t *testing.T) {
	got := FormatCues("read the paperback on the train, then listened to the audiobook")
	want := []string{FormatAudiobook, FormatPaperback}
	if len(got) != len(want) {
		t.Fatalf("FormatCues = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("FormatCues = %v, want the fixed order %v", got, want)
		}
	}
}

func TestFormatCuesNone(t *testing.T) {
	for _, text := range []string{"", "loved it", "the ending was perfect"} {
		if got := FormatCues(text); len(got) != 0 {
			t.Errorf("FormatCues(%q) = %v, want none", text, got)
		}
	}
}

// A cue must match a whole word: "delivered" is not "live", and "candidate" is not "cd".
func TestFormatCuesWholeWords(t *testing.T) {
	for _, text := range []string{"the message was delivered", "a candidate for the best film", "reduced to tears"} {
		if got := FormatCues(text); len(got) != 0 {
			t.Errorf("FormatCues(%q) = %v, want none", text, got)
		}
	}
}

func TestFormatCuesCaseInsensitive(t *testing.T) {
	if got := FormatCues("Watched it at the CINEMA"); len(got) == 0 || got[0] != FormatCinema {
		t.Errorf("FormatCues = %v", got)
	}
}
