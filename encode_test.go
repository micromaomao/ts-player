package main

import (
	"image/color"
	"math/rand"
	"strconv"
	"testing"
)

func Test_frameCell_attrCode(t *testing.T) {
	for i := 0; i < 100; i++ {
		fs := frameCell{}
		fs.chars = []rune("")
		fs.style.bold = rand.Intn(2) == 0
		fs.style.underline = rand.Intn(2) == 0
		fs.style.fg = randColor()
		fs.style.bg = randColor()
		code := fs.attrCode()
		t.Run(strconv.FormatUint(code, 16), func(t *testing.T) {
			nfs := frameCell{}
			nfs.fromAttrCode(code)
			if nfs.style != fs.style {
				t.Errorf("Expected %v, got %v", strconv.FormatUint(code, 16), strconv.FormatUint(nfs.attrCode(), 16))
			}
		})
	}
}

func randColor() color.RGBA {
	col := color.RGBA{}
	col.R = uint8(rand.Intn(256))
	col.G = uint8(rand.Intn(256))
	col.B = uint8(rand.Intn(256))
	col.A = 255
	return col
}

func Test_uint32ToColor(t *testing.T) {
	tests := []struct {
		num  uint32
		want color.RGBA
	}{
		{
			num: 0xFDF6E3, want: color.RGBA{253, 246, 227, 255},
		},
	}
	for _, tt := range tests {
		t.Run(strconv.FormatUint(uint64(tt.num), 16), func(t *testing.T) {
			if got := uint32ToColor(tt.num); got != tt.want {
				t.Errorf("uint32ToColor() = %v, want %v", got, tt.want)
			}
		})
	}
}
