package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"github.com/dustin/go-humanize"
	"github.com/golang/protobuf/proto"
	"github.com/mattn/go-libvterm"
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
	fOut, err := os.OpenFile(opt.output, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(fmt.Errorf("%v when opening %v for writing", err.Error(), opt.output))
	}
	err = fOut.Truncate(0)
	if err != nil {
		panic(err)
	}
	fOut.Seek(0, os.SEEK_SET)

	vt := vterm.New(opt.stage.rows, opt.stage.cols)
	defer vt.Close()
	e := &encoderState{}
	e.t = vt
	e.size.rows = opt.stage.rows
	e.size.cols = opt.stage.cols
	e.pass = encoderPassDictBuilding

	os.Stderr.WriteString("\n")

	fTiming.Seek(0, os.SEEK_SET)
	bTiming.Reset(fTiming)
	fScript.Seek(0, os.SEEK_SET)
	e.resetVT(opt)
	dictSamples := make([][]byte, 0, 1000)
	bytesStored := 0
	tsEncodeFramesPass(0.2, bTiming, fScript, os.Stderr, func(f *frame) {
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
	}, "Collecting")
	e.dict = gozstd.BuildDict(dictSamples, 1024*1024*5)
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
	tsEncodeFramesPass(float64(opt.fps), bTiming, fScript, os.Stderr, func(f *frame) {
		fContent := e.inputToFrameContent(f.data)
		e.writeFrame(f, fContent)
	}, "Encoding")

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
	e.t.ObtainState().SetDefaultColors(opt.stage.initFg, opt.stage.initBg)
}

type frameCallback func(f *frame)

func tsEncodeFramesPass(fps float64, bTiming *bufio.Reader, fScript *os.File, bufStatusOut io.Writer, cb frameCallback, stageText string) {
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
			n, err := fScript.ReadAt(buf, int64(offset))
			if err != nil && err != io.EOF {
				panic(err)
			}
			cb(&frame{fnum, totalSec, fsec, buf[0:n]})
			if fnum%5 == 0 {
				fmt.Fprintf(bufStatusOut, "\r\033[1A\033[2K%v frame %v, t=%vs read=%v\n", stageText, fnum, math.Round(totalSec*10)/10, humanize.Bytes(offset))
			}
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

type encoderPass uint8

const (
	encoderPassDictBuilding encoderPass = 1
	encoderPassEncoding     encoderPass = 2
)

type sizeStruct struct {
	rows, cols int
}

type encoderState struct {
	t                    *vterm.VTerm
	perviousFrameContent frameContent
	fOutput              *os.File
	size                 sizeStruct
	pass                 encoderPass
	dict                 []byte
	cdict                *gozstd.CDict

	fileHeader       *ITSHeader
	headerOffset     uint64
	headerLen        int
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
		fg        color.RGBA // =bg if reverse
		bg        color.RGBA // =fg if reverse
		bold      bool       // bold | blink
		underline bool
	}
}

const (
	cellAttrcodeBold      uint64 = 1
	cellAttrcodeUnderline uint64 = 2
)

func (c *frameCell) styleFromAttrs(attrs *vterm.Attrs, bg, fg color.RGBA) {
	if attrs.Blink > 0 || attrs.Bold > 0 || attrs.Italic > 0 {
		c.style.bold = true
	}
	if attrs.Underline > 0 {
		c.style.underline = true
	}
	c.style.bg = bg
	c.style.fg = fg
}

func (c *frameCell) attrCode() uint64 {
	//    7  6  5  4  3  2  1  0
	// 0x 00 RR GG BB rr gg bb bu
	//      |---fg---|---bg---|fontattr
	fR := c.style.fg.R
	fG := c.style.fg.G
	fB := c.style.fg.B
	bR := c.style.bg.R
	bG := c.style.bg.G
	bB := c.style.bg.B
	var num uint64 = 0
	num += uint64(fR) << (8 * 6)
	num += uint64(fG) << (8 * 5)
	num += uint64(fB) << (8 * 4)
	num += uint64(bR) << (8 * 3)
	num += uint64(bG) << (8 * 2)
	num += uint64(bB) << (8 * 1)
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
	code >>= 8
	bB := uint8(code % 256)
	code >>= 8
	bG := uint8(code % 256)
	code >>= 8
	bR := uint8(code % 256)
	code >>= 8
	fB := uint8(code % 256)
	code >>= 8
	fG := uint8(code % 256)
	code >>= 8
	fR := uint8(code % 256)
	c.style.fg = color.RGBA{R: fR, G: fG, B: fB, A: 255}
	c.style.bg = color.RGBA{R: bR, G: bG, B: bB, A: 255}
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
	return fmt.Sprintf("\033[48;2;%d;%d;%dm\033[38;2;%d;%d;%dm%v%v%v", c.style.bg.R, c.style.bg.G, c.style.bg.B, c.style.fg.R, c.style.fg.G, c.style.fg.B, bold, underline, string(c.chars))
}

func (e *encoderState) newFrameContent() frameContent {
	return make(frameContent, e.size.rows*e.size.cols)
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
	e.t.Write(input)
	drainBuf := make([]byte, 100)
	for {
		n, err := e.t.Read(drainBuf)
		if err != nil || n < len(drainBuf) {
			break
		}
	}

	fc := e.newFrameContent()
	vtScr := e.t.ObtainScreen()
	for row := 0; row < e.size.rows; row++ {
		for col := 0; col < e.size.cols; col++ {
			cell := frameCell{}
			termCell, err := vtScr.GetCellAt(row, col)
			if err != nil {
				panic(err)
			}
			cell.chars = termCell.Chars()
			cell.styleFromAttrs(termCell.Attrs(), termCell.Bg().(color.RGBA), termCell.Fg().(color.RGBA))
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
	compressedDict := gozstd.Compress(nil, e.dict)
	e.fileHeader.CompressionDict = compressedDict
	e.fileHeader.FirstFrameOffset = (1 << 64) - 1
	e.fileHeader.IndexOffset = (1 << 64) - 1
	headerLen := proto.Size(e.fileHeader)
	e.headerLen = headerLen
	binary.Write(e.fOutput, binary.BigEndian, uint32(headerLen))
	e.offset += 4
	e.headerOffset = e.offset
	e.fOutput.Write(make([]byte, headerLen))
	e.offset += uint64(headerLen)
	e.firstFrameOffset = e.offset

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
	compressedBuf := gozstd.CompressDict(nil, buf, e.cdict)

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
	if len(headerBuf) != e.headerLen {
		panic(fmt.Errorf("length changed from %v to %v", e.headerLen, len(headerBuf)))
	}
	e.fOutput.WriteAt(headerBuf, int64(e.headerOffset))

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