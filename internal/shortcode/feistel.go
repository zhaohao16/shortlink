package shortcode

// 4 轮 16 位半块 Feistel 网络。
// Feistel 结构保证 Mix32 对 uint32 是双射：不同 seq 必得不同输出，
// 因此创建短链不需要查询查重。
var roundKeys = [4]uint16{0x1B2C, 0x5D7E, 0x9A31, 0xC4F8}

// roundF 是任意 16 位混合函数，双射性质由 Feistel 结构提供，与 F 本身无关。
func roundF(v, k uint16) uint16 {
	h := uint32(v)*0x9E37 + uint32(k)
	h ^= h >> 7
	h *= 0x85EB
	h ^= h >> 13
	h *= 0xC2B2
	h ^= h >> 16
	return uint16(h)
}

// Mix32 打散：把相邻 seq 映射到输出空间中相距很远的位置，使短码不可枚举。
func Mix32(x uint32) uint32 {
	l, r := uint16(x>>16), uint16(x)
	for i := 0; i < 4; i++ {
		l, r = r, l^roundF(r, roundKeys[i])
	}
	return uint32(l)<<16 | uint32(r)
}

// Unmix32 是 Mix32 的逆。跳转链路不依赖它，保留用于排障时反解 seq。
func Unmix32(y uint32) uint32 {
	l, r := uint16(y>>16), uint16(y)
	for i := 3; i >= 0; i-- {
		l, r = r^roundF(l, roundKeys[i]), l
	}
	return uint32(l)<<16 | uint32(r)
}
