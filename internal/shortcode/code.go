package shortcode

import (
	"errors"
	"math"
)

const (
	// codeMod = 62^6，短码空间大小。
	codeMod = uint64(62 * 62 * 62 * 62 * 62 * 62)
	// codeOffset = 2^32，把输出推高到 62^5 之上，使短码长度恒为 6。
	codeOffset = uint64(1) << 32
	// CodeLen 是短码固定长度。
	CodeLen = 6
)

// ErrSeqOverflow 表示序列号超出发号容量（2^32 条）。
var ErrSeqOverflow = errors.New("shortcode: seq exceeds 32-bit capacity")

// Encode 把单调自增 seq 转成定长 6 位短码。
// 全链路是双射运算（Feistel 双射 + 模加偏移），因此输出天然唯一。
func Encode(seq uint64) (string, error) {
	if seq > math.MaxUint32 {
		return "", ErrSeqOverflow
	}
	y := (uint64(Mix32(uint32(seq))) + codeOffset) % codeMod
	return encodeBase62(y), nil
}

// Decode 反解短码为原始 seq，仅用于排障。
func Decode(code string) (uint64, error) {
	n, err := decodeBase62(code)
	if err != nil {
		return 0, err
	}
	if n < codeOffset {
		return 0, errors.New("shortcode: code out of range")
	}
	return uint64(Unmix32(uint32(n - codeOffset))), nil
}

// IsValidCode 判断字符串是否符合短码格式（6 位 base62）。
func IsValidCode(s string) bool {
	if len(s) != CodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if alphabetIndex[s[i]] < 0 {
			return false
		}
	}
	return true
}
