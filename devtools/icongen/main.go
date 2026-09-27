// 生成 cops 托盘图标（32×32 无压缩 ICO：蓝底圆角方 + 白色双向切换箭头）。
// 输出: internal/tray/icon.ico
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

const size = 32

// pixel 返回 (x,y) 处的 BGRA 像素。
func pixel(x, y int) (b, g, r, a byte) {
	const rad = 7
	inside := true
	if x < rad && y < rad {
		inside = dist2(x, y, rad, rad) <= rad*rad
	} else if x >= size-rad && y < rad {
		inside = dist2(x, y, size-1-rad, rad) <= rad*rad
	} else if x < rad && y >= size-rad {
		inside = dist2(x, y, rad, size-1-rad) <= rad*rad
	} else if x >= size-rad && y >= size-rad {
		inside = dist2(x, y, size-1-rad, size-1-rad) <= rad*rad
	}
	if !inside {
		return 0, 0, 0, 0
	}
	t := float64(y) / float64(size-1)
	r = byte(0x4F + (0x2B-0x4F)*t)
	g = byte(0x8C + (0x5F-0x8C)*t)
	b = byte(0xFF + (0xD9-0xFF)*t)

	white := func() (byte, byte, byte, byte) { return 255, 255, 255, 255 }

	// 上箭头（向右），中心 y=12
	if y >= 10 && y <= 13 && x >= 7 && x <= 20 {
		return white()
	}
	if x > 20 && x <= 25 {
		ext := (25 - x) * 3 / 5
		if abs(y-12) <= ext {
			return white()
		}
	}
	// 下箭头（向左），中心 y=20
	if y >= 19 && y <= 22 && x >= 11 && x <= 24 {
		return white()
	}
	if x >= 6 && x < 11 {
		ext := (x - 6) * 3 / 5
		if abs(y-20) <= ext {
			return white()
		}
	}
	return b, g, r, 255
}

func dist2(x, y, cx, cy int) int {
	dx, dy := x-cx, y-cy
	return dx*dx + dy*dy
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func buildICO() []byte {
	// XOR 位图（自底向上）
	xor := make([]byte, size*size*4)
	for row := 0; row < size; row++ {
		y := size - 1 - row
		for x := 0; x < size; x++ {
			b, g, r, a := pixel(x, y)
			i := (row*size + x) * 4
			xor[i], xor[i+1], xor[i+2], xor[i+3] = b, g, r, a
		}
	}
	// AND 掩码（全 0，透明度由 alpha 决定），每行 4 字节
	and := make([]byte, size*4)

	bih := make([]byte, 40)
	binary.LittleEndian.PutUint32(bih[0:], 40)
	binary.LittleEndian.PutUint32(bih[4:], uint32(int32(size)))
	binary.LittleEndian.PutUint32(bih[8:], uint32(int32(size*2))) // XOR+AND 双倍高
	binary.LittleEndian.PutUint16(bih[12:], 1)
	binary.LittleEndian.PutUint16(bih[14:], 32)

	img := append(bih, xor...)
	img = append(img, and...)

	ico := make([]byte, 0, 22+len(img))
	ico = append(ico, 0, 0, 1, 0, 1, 0) // reserved, type=icon, count=1
	entry := make([]byte, 16)
	entry[0], entry[1] = byte(size), byte(size)
	entry[4], entry[5] = 1, 0  // planes
	entry[6], entry[7] = 32, 0 // bitcount
	binary.LittleEndian.PutUint32(entry[8:], uint32(len(img)))
	binary.LittleEndian.PutUint32(entry[12:], 22)
	ico = append(ico, entry...)
	ico = append(ico, img...)
	return ico
}

func main() {
	out := filepath.Join("internal", "tray", "icon.ico")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(out, buildICO(), 0o644); err != nil {
		panic(err)
	}
	fi, _ := os.Stat(out)
	fmt.Printf("已生成 %s (%d bytes)\n", out, fi.Size())
}
