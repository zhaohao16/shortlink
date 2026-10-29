package shortcode

import "testing"

func TestBase62RoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 61, 62, 12345, 56800235583}
	for _, n := range cases {
		s := encodeBase62(n)
		got, err := decodeBase62(s)
		if err != nil {
			t.Fatalf("decode %q: %v", s, err)
		}
		if got != n {
			t.Fatalf("round trip %d -> %q -> %d", n, s, got)
		}
	}
}

func TestBase62AlphabetOnly(t *testing.T) {
	for n := uint64(0); n < 10000; n++ {
		for _, r := range encodeBase62(n) {
			ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				t.Fatalf("illegal char %q in encoding of %d", r, n)
			}
		}
	}
}

func TestBase62RejectsIllegalChar(t *testing.T) {
	if _, err := decodeBase62("ab-cd"); err == nil {
		t.Fatal("want error for illegal character")
	}
	if _, err := decodeBase62(""); err == nil {
		t.Fatal("want error for empty string")
	}
}
