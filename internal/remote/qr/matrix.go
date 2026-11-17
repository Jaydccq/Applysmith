package qr

// builder holds the symbol while it is drawn. fn marks the modules that belong
// to a function pattern, which data and masks never touch.
type builder struct {
	ver, size int
	mod, fn   [][]bool
}

func newBuilder(ver int) *builder {
	size := ver*4 + 17
	b := &builder{ver: ver, size: size, mod: grid(size), fn: grid(size)}
	return b
}

func grid(n int) [][]bool {
	g := make([][]bool, n)
	for i := range g {
		g[i] = make([]bool, n)
	}
	return g
}

func (b *builder) set(x, y int, dark bool) {
	b.mod[y][x], b.fn[y][x] = dark, true
}

func (b *builder) drawFunctionPatterns() {
	for i := range b.size {
		b.set(6, i, i%2 == 0)
		b.set(i, 6, i%2 == 0)
	}
	b.finder(3, 3)
	b.finder(b.size-4, 3)
	b.finder(3, b.size-4)
	pos := alignPositions(b.ver)
	for i, x := range pos {
		for j, y := range pos {
			corner := (i == 0 && j == 0) || (i == 0 && j == len(pos)-1) || (i == len(pos)-1 && j == 0)
			if !corner {
				b.alignment(x, y)
			}
		}
	}
	b.formatBits(0)
	b.versionBits()
}

func (b *builder) finder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			x, y := cx+dx, cy+dy
			if x < 0 || y < 0 || x >= b.size || y >= b.size {
				continue
			}
			d := max(abs(dx), abs(dy))
			b.set(x, y, d != 2 && d != 4)
		}
	}
}

func (b *builder) alignment(cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			b.set(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func alignPositions(ver int) []int {
	if ver == 1 {
		return nil
	}
	n := ver/7 + 2
	step := (ver*4 + n*2 + 1) / (n*2 - 2) * 2
	size := ver*4 + 17
	pos := make([]int, n)
	pos[0] = 6
	for i, p := n-1, size-7; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

// formatBits writes the two copies of the format word: level M and the mask.
func (b *builder) formatBits(mask int) {
	data := 0<<3 | mask // level M is 00
	rem := data
	for range 10 {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 != 0 }
	for i := range 6 {
		b.set(8, i, bit(i))
	}
	b.set(8, 7, bit(6))
	b.set(8, 8, bit(7))
	b.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		b.set(14-i, 8, bit(i))
	}
	for i := range 8 {
		b.set(b.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		b.set(8, b.size-15+i, bit(i))
	}
	b.set(8, b.size-8, true)
}

func (b *builder) versionBits() {
	if b.ver < 7 {
		return
	}
	rem := b.ver
	for range 12 {
		rem = rem<<1 ^ (rem>>11)*0x1F25
	}
	bits := b.ver<<12 | rem
	for i := range 18 {
		dark := bits>>i&1 != 0
		a, c := b.size-11+i%3, i/3
		b.set(a, c, dark)
		b.set(c, a, dark)
	}
}

// drawCodewords places the data and error correction words in the zigzag
// order of the standard, skipping function modules.
func (b *builder) drawCodewords(words []byte) {
	i := 0
	for right := b.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range b.size {
			for j := range 2 {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = b.size - 1 - vert
				}
				if !b.fn[y][x] && i < len(words)*8 {
					b.mod[y][x] = words[i>>3]>>(7-i&7)&1 != 0
					i++
				}
			}
		}
	}
}

func (b *builder) mask(m int) {
	for y := range b.size {
		for x := range b.size {
			if b.fn[y][x] {
				continue
			}
			var flip bool
			switch m {
			case 0:
				flip = (x+y)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (x+y)%3 == 0
			case 4:
				flip = (x/3+y/2)%2 == 0
			case 5:
				flip = x*y%2+x*y%3 == 0
			case 6:
				flip = (x*y%2+x*y%3)%2 == 0
			default:
				flip = ((x+y)%2+x*y%3)%2 == 0
			}
			b.mod[y][x] = b.mod[y][x] != flip
		}
	}
}

// finish picks the mask with the lowest penalty and returns the symbol.
func (b *builder) finish() [][]bool {
	best, bestScore := 0, -1
	for m := range 8 {
		b.mask(m)
		b.formatBits(m)
		if s := b.penalty(); bestScore < 0 || s < bestScore {
			best, bestScore = m, s
		}
		b.mask(m)
	}
	b.mask(best)
	b.formatBits(best)
	return b.mod
}

// penalty scores a masked symbol with the four rules of the standard: runs,
// 2x2 blocks, finder-like patterns and dark/light balance.
func (b *builder) penalty() int {
	n := b.size
	score := 0
	line := func(at func(i int) bool) {
		run := 1
		for i := 1; i < n; i++ {
			if at(i) == at(i-1) {
				run++
				continue
			}
			if run >= 5 {
				score += run - 2
			}
			run = 1
		}
		if run >= 5 {
			score += run - 2
		}
		for i := 0; i+11 <= n; i++ {
			if matchFinderLike(at, i, false) {
				score += 40
			}
			if matchFinderLike(at, i, true) {
				score += 40
			}
		}
	}
	dark := 0
	for y := range n {
		line(func(i int) bool { return b.mod[y][i] })
		line(func(i int) bool { return b.mod[i][y] })
		for x := range n {
			if b.mod[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n && b.mod[y][x] == b.mod[y][x+1] && b.mod[y][x] == b.mod[y+1][x] && b.mod[y][x] == b.mod[y+1][x+1] {
				score += 3
			}
		}
	}
	total := n * n
	k := (abs(dark*20-total*10) + total - 1) / total
	return score + (k-1)*10
}

// matchFinderLike reports a 1:1:3:1:1 dark run bordered by four light modules
// on one side, starting at i; reversed looks for the mirrored pattern.
func matchFinderLike(at func(int) bool, i int, reversed bool) bool {
	pattern := [11]bool{true, false, true, true, true, false, true, false, false, false, false}
	for j, want := range pattern {
		k := i + j
		if reversed {
			k = i + 10 - j
		}
		if at(k) != want {
			return false
		}
	}
	return true
}
