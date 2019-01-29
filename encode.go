package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"github.com/dustin/go-humanize"
	"github.com/golang/protobuf/proto"
	"github.com/micromaomao/go-libvterm"
	"github.com/valyala/gozstd"
	"image/color"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

func doOpEncode(opt options) {
	fScript, err := os.OpenFile(opt.script, os.O_RDONLY, 0)
	if err != nil {
		panic(fmt.Errorf("%v when opening %v", err.Error(), opt.script))
	}
	defer fScript.Close()
	fTiming, err := os.OpenFile(opt.timing, os.O_RDONLY, 0)
	if err != nil {
		panic(fmt.Errorf("%v when opening %v", err.Error(), opt.timing))
	}
	bTiming := bufio.NewReader(fTiming)
	defer fTiming.Close()
	fOut, err := os.OpenFile(opt.itsOutput, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(fmt.Errorf("%v when opening %v for writing", err.Error(), opt.itsOutput))
	}
	err = fOut.Truncate(0)
	if err != nil {
		panic(err)
	}
	fOut.Seek(0, os.SEEK_SET)

	vt := vterm.New(opt.bufferSize.rows, opt.bufferSize.cols)
	defer vt.Close()
	e := &encoderState{}
	e.t = vt
	e.size.rows = opt.bufferSize.rows
	e.size.cols = opt.bufferSize.cols

	os.Stderr.WriteString("Determining total time and frame number...\n")
	fTiming.Seek(0, os.SEEK_SET)
	bTiming.Reset(fTiming)
	fScript.Seek(0, os.SEEK_SET)
	var totalFrames, _totalBytesRead uint64
	var totalDuration float64
	tsEncodeFramesPass(float64(opt.fps), bTiming, fScript, func(f *frame, bytesRead uint64) {
		totalFrames = f.index
		totalDuration = f.time + f.duration
		_totalBytesRead = bytesRead
	})
	totalDuration = math.Round(totalDuration)
	totalBytesRead := humanize.Bytes(_totalBytesRead)

	fTiming.Seek(0, os.SEEK_SET)
	bTiming.Reset(fTiming)
	fScript.Seek(0, os.SEEK_SET)
	e.resetVT(opt)
	dictSamples := make([][]byte, 0, 1000)
	bytesStored := 0
	tsEncodeFramesPass(200/totalDuration, bTiming, fScript, func(f *frame, bytesRead uint64) {
		fContent := e.inputToFrameContent(f.data)
		fStruct := e.getFrameStruct(f, fContent)
		buf, err := proto.Marshal(fStruct)
		if err != nil {
			panic(err)
		}
		dictSamples = append(dictSamples, buf)
		bytesStored += len(buf)
		if bytesStored >= 1024*1024*1024*2 /*2Gib*/ {
			fTiming.Seek(0, os.SEEK_END)
			bTiming.Reset(fTiming)
		}
		fmt.Fprintf(os.Stderr, "\r\033[1A\033[2KCollecting frames for compression dict (%v%%), t=%vs of %vs read=%v of %v\n", math.Round((float64(f.index)/200)*100), math.Round((f.time+f.duration)*10)/10, totalDuration, humanize.Bytes(bytesRead), totalBytesRead)
	})
	e.dict = gozstd.BuildDict(dictSamples, len(dictSamples)*20)
	e.cdict, err = gozstd.NewCDict(e.dict)
	if err != nil {
		panic(err)
	}
	dictSamples = nil

	fTiming.Seek(0, os.SEEK_SET)
	bTiming.Reset(fTiming)
	fScript.Seek(0, os.SEEK_SET)
	e.resetVT(opt)
	e.initOutputFile(fOut)
	tsEncodeFramesPass(float64(opt.fps), bTiming, fScript, func(f *frame, bytesRead uint64) {
		fContent := e.inputToFrameContent(f.data)
		e.writeFrame(f, fContent)
		fmt.Fprintf(os.Stderr, "\r\033[1A\033[2KEncoding frame %v of %v, t=%vs of %vs read=%v of %v\n", f.index, totalFrames, math.Round((f.time+f.duration)*10)/10, totalDuration, humanize.Bytes(bytesRead), totalBytesRead)
	})

	stat, err := fScript.Stat()
	if err == nil {
		fmt.Fprintf(os.Stderr, "\r\033[1A\033[2KFinalizing... read=%v\n", humanize.Bytes(uint64(stat.Size())))
	}
	e.finalize()
	fOut.Close()
}

func (e *encoderState) resetVT(opt options) {
	e.t.SetUTF8(true)
	vtScr := e.t.ObtainScreen()
	vtScr.Reset(true)
	vtScr.EnableAltScreen(true)
	// tState := e.t.ObtainState()
	// TODO FIXME
	// tState.SetDefaultColors(vterm.NewVTermColorRGB(uint32ToColor(opt.stage.indexedColors[0])), vterm.NewVTermColorRGB(uint32ToColor(opt.stage.indexedColors[1])))
	for i := 2; i < 18; i++ {
		// col := uint32ToColor(opt.stage.indexedColors[i])
		// tState.SetPaletteColor(i-2, vterm.NewVTermColorRGB(col))
	}
	e.t.Write([]byte("\033[0m\033[2J"))
}

func uint32ToColor(i uint32) color.RGBA {
	b := uint8(i % 256)
	i >>= 8
	g := uint8(i % 256)
	i >>= 8
	r := uint8(i % 256)
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

type frameCallback func(f *frame, bytesRead uint64)

func tsEncodeFramesPass(fps float64, bTiming *bufio.Reader, fScript *os.File, cb frameCallback) {
	// figure out the byte offset of the first \n. The first line of the script file is to be ignored.
	scriptLineProb := bufio.NewReaderSize(fScript, 10000)
	firstLine, err := scriptLineProb.ReadBytes('\n')
	if err != nil && err != io.EOF {
		panic(err)
	}
	startFromByteOffset := uint64(len(firstLine))
	var offset, flen uint64
	var fsec, totalSec float64
	var fnum uint64
	spf := 1 / fps
	for {
		tline, err := bTiming.ReadString(byte('\x0a'))
		if len(tline) == 0 && err == io.EOF {
			break
		} else {
			err = nil
		}
		if err != nil {
			panic(err)
		}
		l := strings.Split(strings.TrimRight(tline, "\n"), " ")
		sec, err := strconv.ParseFloat(l[0], 64)
		if err != nil {
			panic(err)
		}
		step, err := strconv.ParseUint(l[1], 10, 64)
		if err != nil {
			panic(err)
		}
		fsec += sec
		flen += uint64(step)
		if fsec >= spf {
			buf := make([]byte, flen)
			n, err := fScript.ReadAt(buf, int64(offset+startFromByteOffset))
			if err != nil && err != io.EOF {
				panic(err)
			}
			cb(&frame{fnum, totalSec, fsec, buf[0:n]}, offset+startFromByteOffset+flen)
			fnum++
			totalSec += fsec
			fsec = 0
			offset += flen
			flen = 0
			if err == io.EOF {
				fmt.Fprintf(os.Stderr, "\rPermature EOF\n\n\r")
				break
			}
		}
	}
}

type sizeStruct struct {
	rows, cols int
}

type encoderState struct {
	t                    *vterm.VTerm
	perviousFrameContent frameContent
	fOutput              *os.File
	size                 sizeStruct
	dict                 []byte
	cdict                *gozstd.CDict

	fileHeader       *ITSHeader
	headerOffset     uint64
	maxHeaderLen     int
	offset           uint64
	firstFrameOffset uint64
	index            *ITSIndex
}

const FileMagic = "\x01ITS-PROTO3"

type frame struct {
	index    uint64
	time     float64
	duration float64
	data     []byte
}
type frameContent []frameCell
type frameCell struct {
	chars []rune
	style struct {
		fg        vterm.VTermColor // =bg if reverse
		bg        vterm.VTermColor // =fg if reverse
		bold      bool             // bold | blink
		underline bool
	}
}

const (
	cellAttrcodeBold           uint64 = 1
	cellAttrcodeUnderline      uint64 = 2
	cellAttrcodeFgIndexedColor uint64 = 1 << (8 * 7)
	cellAttrcodeBgIndexedColor uint64 = 1 << (8*7 + 1)
)

func (c *frameCell) styleFromAttrs(attrs *vterm.Attrs, bg, fg vterm.VTermColor) {
	if attrs.Blink > 0 || attrs.Bold > 0 || attrs.Italic > 0 {
		c.style.bold = true
	}
	if attrs.Underline > 0 {
		c.style.underline = true
	}
	c.style.bg = bg
	c.style.fg = fg
	if attrs.Reverse > 0 {
		c.style.bg = fg
		c.style.fg = bg
	}
}

func (c *frameCell) attrCode() uint64 {
	//    7  6  5  4  3  2  1  0
	// 0x 00 RR GG BB rr gg bb bu
	//      |---fg---|---bg---|fontattr
	var num uint64 = 0
	if c.style.fg.IsRGB() {
		fR, fG, fB, _ := c.style.fg.GetRGB()
		num += uint64(fR) << (8 * 6)
		num += uint64(fG) << (8 * 5)
		num += uint64(fB) << (8 * 4)
	} else {
		index, _ := c.style.fg.GetIndex()
		num += uint64(index) << (8 * 4)
		num |= cellAttrcodeFgIndexedColor
	}
	if c.style.bg.IsRGB() {
		bR, bG, bB, _ := c.style.bg.GetRGB()
		num += uint64(bR) << (8 * 3)
		num += uint64(bG) << (8 * 2)
		num += uint64(bB) << (8 * 1)
	} else {
		index, _ := c.style.bg.GetIndex()
		num += uint64(index) << (8 * 1)
		num |= cellAttrcodeBgIndexedColor
	}
	if c.style.bold {
		num |= cellAttrcodeBold
	}
	if c.style.underline {
		num |= cellAttrcodeUnderline
	}

	return num
}

func (c *frameCell) fromAttrCode(code uint64) {
	if code&cellAttrcodeBold > 0 {
		c.style.bold = true
	}
	if code&cellAttrcodeUnderline > 0 {
		c.style.underline = true
	}
	var fgIndexed, bgIndexed bool
	if code&cellAttrcodeFgIndexedColor > 0 {
		fgIndexed = true
	}
	if code&cellAttrcodeBgIndexedColor > 0 {
		bgIndexed = true
	}
	code >>= 8
	bB := uint8(code % 256)
	bIndex := bB
	code >>= 8
	bG := uint8(code % 256)
	code >>= 8
	bR := uint8(code % 256)
	code >>= 8
	fB := uint8(code % 256)
	fIndex := fB
	code >>= 8
	fG := uint8(code % 256)
	code >>= 8
	fR := uint8(code % 256)
	if !fgIndexed {
		c.style.fg = vterm.NewVTermColorRGB(color.RGBA{R: fR, G: fG, B: fB, A: 255})
	} else {
		c.style.fg = vterm.NewVTermColorIndexed(fIndex)
	}
	if !bgIndexed {
		c.style.bg = vterm.NewVTermColorRGB(color.RGBA{R: bR, G: bG, B: bB, A: 255})
	} else {
		c.style.bg = vterm.NewVTermColorIndexed(bIndex)
	}
}

func (c *frameCell) toOutput() string {
	bold := "\033[22m"
	underline := "\033[24m"
	if c.style.bold {
		bold = "\033[1m"
	}
	if c.style.underline {
		underline = "\033[4m"
	}
	bgCode := ""
	if c.style.bg.IsRGB() {
		r, g, b, _ := c.style.bg.GetRGB()
		bgCode = fmt.Sprintf("\033[48;2;%d;%d;%dm", r, g, b)
	} else {
		index, _ := c.style.bg.GetIndex()
		bgCode = fmt.Sprintf("\033[48;5;%dm", index)
	}
	fgCode := ""
	if c.style.fg.IsRGB() {
		r, g, b, _ := c.style.fg.GetRGB()
		fgCode = fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
	} else {
		index, _ := c.style.fg.GetIndex()
		fgCode = fmt.Sprintf("\033[38;5;%dm", index)
	}
	return bgCode + fgCode + bold + underline + string(c.chars)
}

func (e *encoderState) newFrameContent() frameContent {
	fc := make(frameContent, e.size.rows*e.size.cols)
	for i := 0; i < len(fc); i++ {
		fc[i].chars = []rune{' '}
	}
	return fc
}
func (f *frameContent) getCellAt(row, col int, size *sizeStruct) *frameCell {
	index := size.cols*row + col
	return &([]frameCell(*f)[index])
}
func (f *frameContent) setCellAt(row, col int, cell frameCell, size *sizeStruct) {
	index := size.cols*row + col
	[]frameCell(*f)[index] = cell
}

func (e *encoderState) inputToFrameContent(input []byte) frameContent {
	return e.inputToFrameContentSize(input, e.size)
}

func (e *encoderState) inputToFrameContentSize(input []byte, ctSz sizeStruct) frameContent {
	pervRows, pervCols := e.t.Size()
	if ctSz.rows != pervRows || ctSz.cols != pervCols {
		e.t.SetSize(ctSz.rows, ctSz.cols)
	}
	stepSize := 2000000
	for i := 0; i < len(input); i += stepSize {
		// otherwise it segfaults.
		end := i + stepSize
		if end > len(input) {
			end = len(input)
		}
		e.t.Write(input[i:end])
	}
	drainBuf := make([]byte, 1000)
	for {
		n, err := e.t.Read(drainBuf)
		if err != nil || n < len(drainBuf) {
			break
		}
	}

	fc := e.newFrameContent()
	vtScr := e.t.ObtainScreen()
	for row := 0; row < ctSz.rows; row++ {
		for col := 0; col < ctSz.cols; col++ {
			cell := frameCell{}
			termCell, err := vtScr.GetCellAt(row, col)
			if err != nil {
				panic(err)
			}
			cell.chars = termCell.Chars()
			if len(cell.chars) > 0 && cell.chars[len(cell.chars)-1] == 0 {
				cell.chars = cell.chars[0 : len(cell.chars)-1]
			}
			if len(cell.chars) == 0 {
				cell.chars = []rune{' '}
			}
			cell.styleFromAttrs(termCell.Attrs(), termCell.Bg(), termCell.Fg())
			fc.setCellAt(row, col, cell, &e.size)
		}
	}
	return fc
}

func (e *encoderState) initOutputFile(fOutput *os.File) {
	e.fOutput = fOutput
	e.fOutput.Seek(0, os.SEEK_SET)
	e.fOutput.Write([]byte(FileMagic))
	e.offset = uint64(len(FileMagic))
	e.fileHeader = &ITSHeader{}
	e.fileHeader.Version = 1
	// TODO set timestamp
	e.fileHeader.Timestamp = 0
	e.fileHeader.Rows = uint32(e.size.rows)
	e.fileHeader.Cols = uint32(e.size.cols)
	e.fileHeader.CompressionMode = ITSHeader_COMPRESSION_ZSTD
	if e.dict != nil {
		compressedDict := gozstd.Compress(nil, e.dict)
		e.fileHeader.CompressionDict = compressedDict
	} else {
		e.fileHeader.CompressionDict = []byte{}
	}
	e.fileHeader.FirstFrameOffset = (1 << 64) - 1
	e.fileHeader.IndexOffset = (1 << 64) - 1
	e.maxHeaderLen = proto.Size(e.fileHeader)
	e.headerOffset = e.offset
	binary.Write(e.fOutput, binary.BigEndian, uint32(e.maxHeaderLen))
	e.offset += 4
	e.offset += uint64(e.maxHeaderLen)
	e.firstFrameOffset = e.offset
	e.fileHeader.FirstFrameOffset = e.firstFrameOffset
	e.fileHeader.IndexOffset = 0
	hBuf, err := proto.Marshal(e.fileHeader)
	if err != nil {
		panic(err)
	}
	if len(hBuf) > e.maxHeaderLen {
		panic("headerLen increased.")
	}
	e.fOutput.Seek(int64(e.headerOffset), os.SEEK_SET)
	binary.Write(e.fOutput, binary.BigEndian, uint32(len(hBuf)))
	e.fOutput.Write(hBuf)
	e.fOutput.Seek(int64(e.offset), os.SEEK_SET)

	e.index = &ITSIndex{}
	e.index.Count = 0
	e.index.Frames = make([]*ITSIndex_FrameIndex, 0, 100)
}

func (e *encoderState) getFrameStruct(fi *frame, ct frameContent) *ITSFrame {
	frameStruct := &ITSFrame{}
	frameStruct.FrameId = uint64(fi.index)
	frameStruct.TimeOffset = fi.time
	frameStruct.Duration = fi.duration
	frameStruct.Type = ITSFrame_FRAMETYPE_K
	body := &ITSFrame_BodyK{}
	body.BodyK = &ITSFrame_KFrame{}
	frameStruct.Body = body
	cellNum := e.size.rows * e.size.cols
	contentArr := make([]string, 0, cellNum)
	attrsArr := make([]uint64, 0, cellNum)

	for row := 0; row < e.size.rows; row++ {
		for col := 0; col < e.size.cols; col++ {
			memcell := ct.getCellAt(row, col, &e.size)
			contentArr = append(contentArr, string(memcell.chars))
			attrsArr = append(attrsArr, memcell.attrCode())
		}
	}

	body.BodyK.Contents = contentArr
	body.BodyK.Attrs = attrsArr
	return frameStruct
}

func (e *encoderState) writeFrame(frameInfo *frame, currentFrameContent frameContent) {
	e.index.Count++
	indexFrame := &ITSIndex_FrameIndex{}
	indexFrame.TimeOffset = frameInfo.time
	indexFrame.ByteOffset = e.offset
	e.index.Frames = append(e.index.Frames, indexFrame)

	frameStruct := e.getFrameStruct(frameInfo, currentFrameContent)
	buf, err := proto.Marshal(frameStruct)
	if err != nil {
		panic(err)
	}
	var compressedBuf []byte
	if e.cdict != nil {
		compressedBuf = gozstd.CompressDict(nil, buf, e.cdict)
	} else {
		compressedBuf = gozstd.Compress(nil, buf)
	}

	length := uint32(len(compressedBuf))
	binary.Write(e.fOutput, binary.BigEndian, length)
	e.offset += 4
	e.fOutput.Write(compressedBuf)
	e.offset += uint64(length)
}

func (e *encoderState) finalize() {
	indexOffset := e.offset

	e.fileHeader.IndexOffset = indexOffset
	e.fileHeader.FirstFrameOffset = e.firstFrameOffset
	headerBuf, err := proto.Marshal(e.fileHeader)
	if err != nil {
		panic(err)
	}
	if len(headerBuf) > e.maxHeaderLen {
		panic(fmt.Errorf("header length increased from %v to %v", e.maxHeaderLen, len(headerBuf)))
	}
	e.fOutput.Seek(int64(e.headerOffset), os.SEEK_SET)
	binary.Write(e.fOutput, binary.BigEndian, uint32(len(headerBuf)))
	e.fOutput.Write(headerBuf)

	e.fOutput.Seek(int64(indexOffset), os.SEEK_SET)
	indexBuf, err := proto.Marshal(e.index)
	if err != nil {
		panic(err)
	}
	compressedIndex := gozstd.Compress(nil, indexBuf)
	binary.Write(e.fOutput, binary.BigEndian, uint64(len(compressedIndex)))
	e.fOutput.Write(compressedIndex)
	e.fOutput.Close()
}
