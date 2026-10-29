package shortcode

import "testing"

// TestMix32IsBijective 验证 Feistel 打散是双射：不同输入必得不同输出。
// 这是"创建短链无需查重"的前提。
func TestMix32IsBijective(t *testing.T) {
	const n = 200000
	seen := make(map[uint32]struct{}, n)
	for x := uint32(0); x < n; x++ {
		y := Mix32(x)
		if _, dup := seen[y]; dup {
			t.Fatalf("collision: Mix32(%d) reused", x)
		}
		seen[y] = struct{}{}
	}
}

func TestMix32RoundTrip(t *testing.T) {
	for _, x := range []uint32{0, 1, 100000, 1 << 31, ^uint32(0)} {
		if got := Unmix32(Mix32(x)); got != x {
			t.Fatalf("unmix(mix(%d)) = %d", x, got)
		}
	}
}

// TestCodeIsAlwaysSixChars 验证偏移量把输出推到 62^5 之上，短码长度恒定。
func TestCodeIsAlwaysSixChars(t *testing.T) {
	for seq := uint64(100000); seq < 100000+50000; seq++ {
		code, err := Encode(seq)
		if err != nil {
			t.Fatalf("encode %d: %v", seq, err)
		}
		if len(code) != CodeLen {
			t.Fatalf("encode %d = %q, want len %d", seq, code, CodeLen)
		}
	}
}

func TestCodeUnique(t *testing.T) {
	seen := make(map[string]uint64)
	for seq := uint64(100000); seq < 100000+200000; seq++ {
		code, _ := Encode(seq)
		if prev, dup := seen[code]; dup {
			t.Fatalf("code %q collision between seq %d and %d", code, prev, seq)
		}
		seen[code] = seq
	}
}

// TestCodeSnapshot 固定快照：算法（含轮密钥、偏移）被误改时立即失败。
func TestCodeSnapshot(t *testing.T) {
	want := []struct {
		seq  uint64
		code string
	}{
		{100000, "8vKuoi"},
		{100001, "5WqvM7"},
		{100002, "7shSqW"},
	}
	mismatch := false
	for _, w := range want {
		got, err := Encode(w.seq)
		if err != nil {
			t.Fatalf("encode %d: %v", w.seq, err)
		}
		if got != w.code {
			mismatch = true
			t.Logf("seq %d: got %q want %q", w.seq, got, w.code)
		}
	}
	if mismatch {
		t.Fatal("snapshot mismatch")
	}
}

func TestEncodeRejectsOverflow(t *testing.T) {
	if _, err := Encode(1 << 32); err == nil {
		t.Fatal("want error for seq >= 2^32")
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	for _, seq := range []uint64{0, 1, 100000, 4294967295} {
		code, err := Encode(seq)
		if err != nil {
			t.Fatalf("encode %d: %v", seq, err)
		}
		got, err := Decode(code)
		if err != nil {
			t.Fatalf("decode %q: %v", code, err)
		}
		if got != seq {
			t.Fatalf("decode(encode(%d)) = %d", seq, got)
		}
	}
}

func TestDecodeRejectsOutOfRange(t *testing.T) {
	// "zzzzzz" 解出来大于 codeOffset+2^32，属于不可能是本系统生成的值，
	// 必须报错而不是静默截断成垃圾 seq。
	if _, err := Decode("zzzzzz"); err == nil {
		t.Fatal("want error for out-of-range code")
	}
}

func TestIsValidCode(t *testing.T) {
	good := []string{"aB3c9F", "000000", "zzzzzz"}
	bad := []string{"", "abcde", "abcdefg", "aB3c9-", "aB3c9!", "../api", "aB3c9/"}
	for _, s := range good {
		if !IsValidCode(s) {
			t.Fatalf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if IsValidCode(s) {
			t.Fatalf("%q should be invalid", s)
		}
	}
}
