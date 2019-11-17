package main

import (
	"fmt"
	"github.com/mattn/go-isatty"
	"image/color"
	"image/png"
	"os"
	"strconv"
	"strings"
)

func doOpGetColorProfile(opt options) {
	if !opt.evenIfNotTty && (!isatty.IsTerminal(0) || !isatty.IsTerminal(1)) {
		fmt.Fprintf(os.Stderr, "Stdin and/or stdout are not terminals!\n")
		os.Exit(1)
	}
	doPrintPattern()
}

const patternWidth = 34
const patternHeight = 10

var finderBg = color.RGBA{0, 0, 0, 0xff}
var finderFg = color.RGBA{0xff, 0x00, 0xff, 0xff}

func doPrintPattern() {
	termSize := termGetSize()
	startX := termSize.cols/2 - patternWidth/2
	startY := termSize.rows / 4
	outTo := os.Stdout
	fmt.Fprintf(outTo, "\033[7l\033[1;1H\033[2J\033[%v;%vH", startY+1, startX+1)
	fmt.Fprintf(outTo, "\033[48;2;%v;%v;%vm\033[38;2;%d;%d;%dm", finderBg.R, finderBg.G, finderBg.B, finderFg.R, finderFg.G, finderFg.B)
	// top pattern
	outTo.WriteString(strings.Repeat("\033[27m \033[7m ", patternWidth/2))
	// bottom pattern
	fmt.Fprintf(outTo, "\033[%d;%dH", startY+1+patternHeight-1, startX+1)
	outTo.WriteString(strings.Repeat("\033[7m \033[27m ", patternWidth/2))
	// left and right pattern
	for y := 0; y < patternHeight; y++ {
		fmt.Fprintf(outTo, "\033[%d;%dH", startY+y+1, startX+1)
		even := fmt.Sprintf("\033[27m \033[%dC\033[7m ", patternWidth-2)
		odd := fmt.Sprintf("\033[7m \033[%dC\033[27m ", patternWidth-2)
		if y%2 == 0 {
			outTo.WriteString(even)
		} else {
			outTo.WriteString(odd)
		}
	}
	fmt.Fprintf(outTo, "\033[%d;%dH", startY+2, startX+2)
	// colors
	rowOff := 1
	colOff := 0
	for i := 0; i < 256; i++ {
		fmt.Fprintf(outTo, "\033[27;48;5;%dm ", i)
		colOff++
		if colOff == patternWidth-2 {
			colOff = 0
			rowOff++
			fmt.Fprintf(outTo, "\033[%d;%dH", startY+1+rowOff, startX+2)
		}
	}
	fmt.Fprintf(outTo, "\033[%d;%dH\033[0;7m ", startY+patternHeight-2, startX+patternWidth)
	fmt.Fprintf(outTo, "\033[%d;%dH\033[0;27m ", startY+patternHeight-1, startX+patternWidth)
	const msg1 = "Take a screenshot of the above pattern and save it as a png image."
	const msg2 = "That image can then be used as a color profile."
	const msg3 = "You don't have to be precise. Some background border is OK."
	const msg4 = "Even a screenshot of the entire screen will be fine."
	fmt.Fprintf(outTo, "\033[%d;%dH\033[0m%v", startY+patternHeight+3, termSize.cols/2-len(msg1)/2, msg1)
	fmt.Fprintf(outTo, "\033[%d;%dH\033[0m%v", startY+patternHeight+4, termSize.cols/2-len(msg2)/2, msg2)
	fmt.Fprintf(outTo, "\033[%d;%dH\033[0m%v", startY+patternHeight+5, termSize.cols/2-len(msg3)/2, msg3)
	fmt.Fprintf(outTo, "\033[%d;%dH\033[0m%v", startY+patternHeight+6, termSize.cols/2-len(msg4)/2, msg4)
	fmt.Fprintf(outTo, "\033[%d;%dH\033[0m", startY+patternHeight+8, 1)
}

type colorProfile struct {
	fg, bg   color.RGBA
	palette [256]color.RGBA
}

func abs(i int) int {
	if i < 0 {
		return -i
	} else {
		return i
	}
}

type lineInfoStruct struct {
	sign         int8
	y            int
	firstSwitchX int
	segmentWidth int
}

func (s lineInfoStruct) String() string {
	return fmt.Sprintf("{sign=%v, y=%v, firstSwitchX=%v, segmentWidth=%v}", s.sign, s.y, s.firstSwitchX, s.segmentWidth)
}

type topBottomStruct struct {
	top    lineInfoStruct
	bottom lineInfoStruct
}

func (tb topBottomStruct) contentHeight() int {
	return tb.bottom.y - tb.top.y - 1
}

func processColorProfile(file string) (cf colorProfile, err error) {
	if !strings.HasSuffix(file, ".png") {
		err = fmt.Errorf("Only png file is supported")
		return
	}
	f, err := os.OpenFile(file, os.O_RDONLY, 0)
	if err != nil {
		return
	}
	img, err := png.Decode(f)
	if err != nil {
		return
	}
	bd := img.Bounds()
	const (
		sign_fg_to_bg   = -1
		sign_bg_to_fg   = 1
		linesign_top    = sign_bg_to_fg // the first switch of the top finder pattern is from bg to fg.
		linesign_bottom = sign_fg_to_bg
	)
	lineInfos := make([]lineInfoStruct, bd.Max.Y-bd.Min.Y)
	signs := make([]int8, bd.Max.X-bd.Min.X)
	switchesAt := make([]int, 0, bd.Max.X-bd.Min.X)
	foundAnyLines := false
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		lineInfos[0].sign = 0
		switchesAt = switchesAt[0:0]
		for x := bd.Min.X + 1; x < bd.Max.X; x++ {
			last := img.At(x-1, y)
			now := img.At(x, y)
			if colorEq(last, finderBg) && colorEq(now, finderFg) {
				switchesAt = append(switchesAt, x)
				signs[x-bd.Min.X] = sign_bg_to_fg
			} else if colorEq(last, finderFg) && colorEq(now, finderBg) {
				switchesAt = append(switchesAt, x)
				signs[x-bd.Min.X] = sign_fg_to_bg
			} else {
				signs[x-bd.Min.X] = 0
			}
		}
		if len(switchesAt) < 33 {
			continue
		}
		// 33 switches in one row of finder pattern
		currentStreak := 1
		currentWidth := switchesAt[1] - switchesAt[0]
		found := false
		var firstI int
		for i := 1; i < len(switchesAt); i++ {
			thisWidth := switchesAt[i] - switchesAt[i-1]
			if thisWidth == currentWidth {
				currentStreak++
				if currentStreak == 33 {
					found = true
					firstI = i - 32
					break
				}
			} else {
				currentStreak = 1
				currentWidth = thisWidth
				i = i - 1
			}
		}
		if !found {
			continue
		}
		firstSwitchSign := signs[switchesAt[firstI]-bd.Min.X]
		lineInfos[y-bd.Min.Y] = lineInfoStruct{
			sign:         firstSwitchSign,
			y:            y - bd.Min.Y,
			firstSwitchX: switchesAt[firstI] - bd.Min.X,
			segmentWidth: currentWidth,
		}
		log("Finder pattern line: %v", lineInfos[y-bd.Min.Y])
		foundAnyLines = true
	}
	if !foundAnyLines {
		err = fmt.Errorf("No pattern recognized")
		return
	}
	tops := make([]lineInfoStruct, 0, len(lineInfos))
	bottoms := make([]lineInfoStruct, 0, len(lineInfos))
	var lastIs int8
	for y := 0; y < len(lineInfos); y++ {
		current := lineInfos[y]
		if current.sign == linesign_top {
			if lastIs == 0 {
				tops = append(tops, current)
			} else if lastIs == linesign_top {
				lastTop := tops[len(tops)-1]
				if lastTop.segmentWidth == current.segmentWidth && lastTop.firstSwitchX == current.firstSwitchX {
					tops[len(tops)-1] = current
				} else {
					tops = append(tops, current)
				}
			}
		}
		lastIs = current.sign
	}
	lastIs = 0
	for y := len(lineInfos) - 1; y >= 0; y-- {
		current := lineInfos[y]
		if current.sign == linesign_bottom {
			if lastIs == 0 {
				bottoms = append(bottoms, current)
			} else if lastIs == linesign_bottom {
				lastBottom := bottoms[len(bottoms)-1]
				if lastBottom.segmentWidth == current.segmentWidth && lastBottom.firstSwitchX == current.firstSwitchX {
					bottoms[len(bottoms)-1] = current
				} else {
					bottoms = append(bottoms, current)
				}
			}
		}
		lastIs = current.sign
	}
	log("tops=%v, bottoms=%v", tops, bottoms)
	topBottomPairs := make([]topBottomStruct, 0, len(tops))
	for currentTopI := 0; currentTopI < len(tops); currentTopI++ {
		t := tops[currentTopI]
		for i := 0; i < len(bottoms); i++ {
			b := bottoms[i]
			if b.segmentWidth == t.segmentWidth && b.firstSwitchX == t.firstSwitchX {
				topBottomPairs = append(topBottomPairs, topBottomStruct{top: t, bottom: b})
			}
		}
	}
	if len(topBottomPairs) == 0 {
		err = fmt.Errorf("No pattern found")
		return
	}
	log("%v top-bottom pairs:", len(topBottomPairs))
	for _, v := range topBottomPairs {
		log("  %v and %v with height=%v,", v.top, v.bottom, v.contentHeight())
	}
	for _, tbPair := range topBottomPairs {
		if tbPair.contentHeight() < patternHeight-2 {
			continue
		}
		rightMostLeftFinderX := tbPair.top.firstSwitchX - 1
		leftFinderY := tbPair.top.y + 1
		if !colorEq(img.At(rightMostLeftFinderX+bd.Min.X, leftFinderY+bd.Min.Y), finderFg) {
			continue
		}
		if !colorEq(img.At(rightMostLeftFinderX+bd.Min.X, leftFinderY-1+bd.Min.Y), finderBg) {
			continue
		}
		var segmentHeight int
		for segmentHeight = 0; segmentHeight < tbPair.contentHeight(); segmentHeight++ {
			if colorEq(img.At(rightMostLeftFinderX+bd.Min.X, leftFinderY+segmentHeight+bd.Min.Y), finderBg) {
				break
			}
		}
		if segmentHeight*(patternHeight-2) != tbPair.contentHeight() {
			continue
		}
		wrong := false
		for yd := 0; yd < tbPair.contentHeight(); yd++ {
			colExpectFg := (yd/segmentHeight)%2 == 0
			colExpect := finderFg
			colExpectInverse := finderBg
			if !colExpectFg {
				colExpect = finderBg
				colExpectInverse = finderFg
			}
			// left
			if !colorEq(img.At(rightMostLeftFinderX+bd.Min.X, leftFinderY+yd+bd.Min.Y), colExpect) {
				wrong = true
				break
			}
			// right
			if yd >= 6*segmentHeight {
				continue
			}
			if !colorEq(img.At(rightMostLeftFinderX+1+(tbPair.top.segmentWidth*32)+bd.Min.X, leftFinderY+yd+bd.Min.Y), colExpectInverse) {
				wrong = true
				break
			}
		}
		if wrong {
			continue
		}
		log("Confirmed segheight=%v, starting to read out data...", segmentHeight)
		contentX := tbPair.top.firstSwitchX
		contentY := leftFinderY
		segmentWidth := tbPair.top.segmentWidth
		i := 0
		for row := 0; row < 8; row++ {
			for col := 0; col < 32; col++ {
				x := contentX + col*segmentWidth
				y := contentY + row*segmentHeight
				col := img.At(x+segmentWidth/2+bd.Min.X, y+segmentHeight/2+bd.Min.Y)
				cf.palette[i] = toRgba(col)
				i++
			}
		}
		fg := img.At(contentX+32*segmentWidth+segmentWidth/2+bd.Min.X, contentY+6*segmentHeight+segmentHeight/2+bd.Min.X)
		bg := img.At(contentX+32*segmentWidth+segmentWidth/2+bd.Min.X, contentY+7*segmentHeight+segmentHeight/2+bd.Min.X)
		cf.fg = toRgba(fg)
		cf.bg = toRgba(bg)
		return
	}
	err = fmt.Errorf("No pattern found")
	return
}

func colorEq(a, b color.Color) bool {
	a1, a2, a3, _ := a.RGBA()
	b1, b2, b3, _ := b.RGBA()
	c1, c2, c3 := int(a1)-int(b1), int(a2)-int(b2), int(a3)-int(b3)
	c1, c2, c3 = abs(c1), abs(c2), abs(c3)
	const thres = 0xffff - 0xfefe
	return c1 < thres && c2 < thres && c3 < thres
}

func toRgba(c color.Color) color.RGBA {
	cRGBA, ok := c.(color.RGBA)
	if ok {
		return cRGBA
	} else {
		ret := color.RGBA{}
		r, g, b, a := c.RGBA()
		ret.R = uint8(r >> 8)
		ret.G = uint8(g >> 8)
		ret.B = uint8(b >> 8)
		ret.A = uint8(a >> 8)
		return ret
	}
}

func rgbaToHex(col color.RGBA) string {
	i := (uint64(col.R) << 16) + (uint64(col.G) << 8) + uint64(col.B)
	hex := strconv.FormatUint(i, 16)
	if len(hex) < 6 {
		hex = strings.Repeat("0", 6-len(hex)) + hex
	}
	return "#" + hex
}

func doOpCheckColorProfile(opt options) {
	profile, err := processColorProfile(opt.colorProfileInput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v.\n", err.Error())
		os.Exit(1)
		return
	}
	fmt.Fprintf(os.Stdout, "fg: %v\nbg: %v\n", rgbaToHex(profile.fg), rgbaToHex(profile.bg))
	for i := 0; i < 16; i++ {
		fmt.Fprintf(os.Stdout, "%d: %v\n", i, rgbaToHex(profile.palette[i]))
	}
	os.Stdout.WriteString("...\n")
}
