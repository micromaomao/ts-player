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
