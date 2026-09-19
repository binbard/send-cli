package telegram

import "testing"

func TestMediaMethod(t *testing.T) {
	tests := map[string]string{
		"photo.JPG": "sendPhoto",
		"clip.mp4":  "sendVideo",
		"data.zip":  "sendDocument",
	}
	for path, want := range tests {
		got, _ := mediaMethod(path)
		if got != want {
			t.Errorf("mediaMethod(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestMatchesBotMarker(t *testing.T) {
	const marker = "/sendcli_ABC123"
	for _, value := range []string{marker, marker + "@example_bot", "  " + marker + "  "} {
		if !matchesBotMarker(value, marker) {
			t.Errorf("%q should match", value)
		}
	}
	if matchesBotMarker("/sendcli_OTHER", marker) {
		t.Fatal("different marker must not match")
	}
}
