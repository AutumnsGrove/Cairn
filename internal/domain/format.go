package domain

import "strings"

// Format is how the work was experienced, which is separate from what it is: an audiobook and a hardback are the
// same work with different formats (PLAN.md 7). The cues here are deterministic, so the same message always
// yields the same candidates; the decisions layer may add a suggestion, but code decides.
const (
	FormatCinema    = "cinema"
	FormatStreaming = "streaming"
	FormatBroadcast = "broadcast"
	FormatAudiobook = "audiobook"
	FormatHardback  = "hardback"
	FormatPaperback = "paperback"
	FormatEbook     = "ebook"
	FormatPhysical  = "physical"
	FormatLive      = "live"
)

// formatCues maps a phrase to the format it implies. The order is the order results are returned in, so the same
// message always produces the same list.
var formatCues = []struct {
	format  string
	phrases []string
}{
	{FormatCinema, []string{"cinema", "big screen", "imax", "at the pictures", "movie theatre", "movie theater"}},
	{FormatStreaming, []string{"netflix", "prime video", "disney plus", "disney+", "hulu", "hbo max", "streaming", "at home", "on my laptop"}},
	{FormatBroadcast, []string{"on tv", "broadcast", "on television", "live tv"}},
	{FormatAudiobook, []string{"audiobook", "audio book", "audible", "listened to", "narrator", "narrated"}},
	{FormatHardback, []string{"hardback", "hardcover"}},
	{FormatPaperback, []string{"paperback"}},
	{FormatEbook, []string{"kindle", "ebook", "e-book", "kobo", "e-reader", "ereader"}},
	{FormatPhysical, []string{"blu-ray", "bluray", "dvd", "vinyl", "4k", "cd"}},
	{FormatLive, []string{"live", "in person", "saw them", "gig", "concert"}},
}

// FormatCues returns the formats a message hints at, in a stable order, without duplicates. An empty result means
// the message did not say, which is normal: the card shows an empty format and the agent may ask.
func FormatCues(text string) []string {
	if len(text) > maxDateScan {
		return nil
	}
	s := strings.ToLower(text)
	var out []string
	for _, cue := range formatCues {
		for _, phrase := range cue.phrases {
			if containsWord(s, phrase) {
				out = append(out, cue.format)
				break
			}
		}
	}
	return out
}
