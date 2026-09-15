package vision

import "testing"

func TestMarkerRoundTrip(t *testing.T) {
	path := "/tmp/screenshots/a.png"
	m := Marker(path)
	if m != "[reasonix-image: /tmp/screenshots/a.png]" {
		t.Errorf("Marker = %q", m)
	}
	got, ok := ParseMarker(m)
	if !ok || got != path {
		t.Errorf("ParseMarker(Marker(%q)) = (%q, %v), want (%q, true)", path, got, ok, path)
	}
}

func TestParseMarkerNoMarker(t *testing.T) {
	if _, ok := ParseMarker("just some text"); ok {
		t.Error("expected false for plain text")
	}
}

func TestParseMarkerEmptyPath(t *testing.T) {
	if _, ok := ParseMarker("[reasonix-image: ]"); ok {
		t.Error("expected false for empty path")
	}
}

func TestParseMarkerIgnoresTrailingText(t *testing.T) {
	text := "[reasonix-image: /foo.png] mime=image/png, 1024 bytes"
	got, ok := ParseMarker(text)
	if !ok || got != "/foo.png" {
		t.Errorf("ParseMarker = (%q, %v), want /foo.png, true", got, ok)
	}
}
