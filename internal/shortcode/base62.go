package shortcode

import "fmt"

// alphabet 是短码字符集：数字 + 小写 + 大写，全部对 URL 安全。
const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// alphabetIndex 把字节反查为字符集下标，非法字符为 -1。
var alphabetIndex [256]int16

func init() {
	for i := range alphabetIndex {
		alphabetIndex[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		alphabetIndex[alphabet[i]] = int16(i)
	}
}

func encodeBase62(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [11]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = alphabet[n%62]
		n /= 62
	}
	return string(buf[i:])
}

func decodeBase62(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("shortcode: empty string")
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		d := alphabetIndex[s[i]]
		if d < 0 {
			return 0, fmt.Errorf("shortcode: invalid character %q", s[i])
		}
		n = n*62 + uint64(d)
	}
	return n, nil
}
