package tools

import "testing"

func TestWindowsTtyInputNormalizer(t *testing.T) {
	var normalizer windowsTtyInputNormalizer
	if got, want := string(normalizer.normalize([]byte("first\n"))), "first\r"; got != want {
		t.Fatalf("lf = %q, want %q", got, want)
	}
	if got, want := string(normalizer.normalize([]byte("second\r"))), "second\r"; got != want {
		t.Fatalf("cr = %q, want %q", got, want)
	}
	if got, want := string(normalizer.normalize([]byte("\nthird\r\n"))), "third\r"; got != want {
		t.Fatalf("split crlf = %q, want %q", got, want)
	}

	input := append([]byte("cafeé 漢字"), '\x08', '\x03')
	got := normalizer.normalize(input)
	want := append([]byte("cafeé 漢字"), '\x7f', '\x03')
	if string(got) != string(want) {
		t.Fatalf("controls = %q, want %q", got, want)
	}
}
