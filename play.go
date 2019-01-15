package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/valyala/gozstd"
	"io"
	"math"
	"os"
	"sync"
)

type decoderState struct {
	size       sizeStruct
	index      *ITSIndex
	compressed bool
	ddict      *gozstd.DDict
	file       *os.File
	readLock   sync.Locker
}

func doOpPlay(opt options) {
	d := initPlayer(opt)
	off := d.searchOffsetForFrame(0)
	var nextOffset uint64 = off
	var prev frameContent = nil
	for {
		var ct frameContent
		var err error
		_, ct, err, nextOffset = d.readFrameFromOffset(nextOffset)
		if err != nil {
			continue
		}
		d.renderFrameTo(prev, ct, os.Stdout)
		prev = ct
	}
}

func initPlayer(opt options) *decoderState {
	fIts, err := os.OpenFile(opt.itsFile, os.O_RDONLY, 0)
	if err != nil {
		panic(err)
	}
	magicBuffer := make([]byte, len(FileMagic))
	n, err := fIts.Read(magicBuffer)
	if err != nil {
		panic(err)
	}
	if n != len(FileMagic) || bytes.Compare(magicBuffer, []byte(FileMagic)) != 0 {
		panic("Not a its file: magic wrong.")
	}
	var headerLen uint32
	binary.Read(fIts, binary.BigEndian, &headerLen)
	if headerLen > 10000 || headerLen < 1 {
		panic("Invalid headerLen")
	}
	headerBuff := make([]byte, headerLen)
	n, err = fIts.Read(headerBuff)
	if uint32(n) < headerLen || err == io.EOF {
		panic("Permature EOF")
	}
	header := &ITSHeader{}
	err = proto.Unmarshal(headerBuff, header)
	if err != nil {
		panic(err)
	}
	if header.GetVersion() != 1 {
		panic("Invalid file version. Please update this player.")
	}

	d := &decoderState{}
	d.size.rows = int(header.GetRows())
	d.size.cols = int(header.GetCols())
	if d.size.rows*d.size.cols <= 0 {
		panic("Invalid dimension")
	}
	indexOffset := header.GetIndexOffset()
	if indexOffset <= 12 {
		panic("Invalid indexOffset")
	}
	_, err = fIts.Seek(int64(indexOffset), os.SEEK_SET)
	if err != nil {
		panic(err)
	}
	var indexLen uint64
	err = binary.Read(fIts, binary.BigEndian, &indexLen)
	if err != nil {
		panic(err)
	}
	fileStat, err := fIts.Stat()
	if err != nil {
		panic(err)
	}
	fileLen := uint64(fileStat.Size())
	if indexOffset+indexLen > fileLen+10000 {
		panic("Invalid indexLen")
	}
	indexBuf := make([]byte, indexLen)
	n, err = fIts.Read(indexBuf)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if uint64(n) < indexLen {
		panic("Permature EOF")
	}
	switch header.GetCompressionMode() {
	case ITSHeader_COMPRESSION_ZSTD:
		d.compressed = true
		dictBuf, err := gozstd.Decompress(nil, header.GetCompressionDict())
		if err != nil {
			panic(err)
		}
		d.ddict, err = gozstd.NewDDict(dictBuf)
		if err != nil {
			panic(err)
		}

		indexBuf, err = gozstd.Decompress(nil, indexBuf)
		if err != nil {
			panic(err)
		}
	case ITSHeader_COMPRESSION_NONE:
		d.compressed = false
	default:
		panic("Unknown compression mode")
	}
	d.index = &ITSIndex{}
	err = proto.Unmarshal(indexBuf, d.index)
	if err != nil {
		panic(err)
	}
	if d.index.GetCount() <= 0 || len(d.index.GetFrames()) <= 0 {
		panic("Empty index")
	}
	if d.index.GetCount() != uint64(len(d.index.GetFrames())) {
		panic("Wrong index count")
	}
	d.file = fIts
	d.readLock = &sync.Mutex{}
	return d
}

func (d *decoderState) searchOffsetForFrame(time float64) uint64 {
	frames := d.index.GetFrames()
	if frames[0].GetTimeOffset()+0.0001 >= time {
		return frames[0].GetByteOffset()
	}
	if frames[len(frames)-1].GetTimeOffset()-0.0001 < time {
		return frames[len(frames)-1].GetByteOffset()
	}
	var i, j int
	j = len(frames)
	for {
		if i >= j || i >= len(frames) {
			return frames[j-1].GetByteOffset()
		}
		mid := (i + j) / 2
		foundTime := frames[mid].GetTimeOffset()
		if math.Abs(foundTime-time) < 0.0001 {
			return frames[mid].GetByteOffset()
		}
		if foundTime < time {
			i = mid + 1
		} else {
			j = mid - 1
		}
	}
}

func (d *decoderState) readFrameFromOffset(byteOffset uint64) (frameInfo *frame, content frameContent, err error, nextOffset uint64) {
	d.readLock.Lock()
	defer d.readLock.Unlock()
	_, err = d.file.Seek(int64(byteOffset), os.SEEK_SET)
	if err != nil {
		return
	}
	var frameByteLen uint32
	err = binary.Read(d.file, binary.BigEndian, &frameByteLen)
	if err != nil {
		return
	}
	if frameByteLen > 1024*1024*50 {
		// gaurd against DNS, arbitrary value.
		err = fmt.Errorf("invalid frame byteLength near %x", byteOffset)
		return
	}
	buf := make([]byte, frameByteLen)
	d.file.Read(buf)
	nextOffset = byteOffset + 4 + uint64(frameByteLen)
	if d.compressed {
		buf, err = gozstd.DecompressDict(nil, buf, d.ddict)
		if err != nil {
			return
		}
	}
	frameStruct := &ITSFrame{}
	err = proto.Unmarshal(buf, frameStruct)
	if err != nil {
		return
	}
	defer func() {
		p := recover()
		if p != nil {
			err = errors.New("panic!")
			fmt.Fprintf(os.Stderr, "%v", p)
		}
	}()
	frameInfo, content = d.decodeFrameStruct(frameStruct)
	return
}

func (d *decoderState) decodeFrameStruct(frameStruct *ITSFrame) (frameInfo *frame, content frameContent) {
	if frameStruct.GetType() != ITSFrame_FRAMETYPE_K {
		panic("Unrecognized frame type")
	}
	finfo := &frame{}
	finfo.index = frameStruct.GetFrameId()
	finfo.time = frameStruct.GetTimeOffset()
	finfo.duration = frameStruct.GetDuration()
	fContent := make(frameContent, d.size.rows*d.size.cols)
	body := frameStruct.GetBodyK()
	i := 0
	for row := 0; row < d.size.rows; row++ {
		for col := 0; col < d.size.cols; col++ {
			cell := frameCell{}
			cell.chars = []rune(body.GetContents()[i])
			cell.fromAttrCode(body.GetAttrs()[i])
			fContent.setCellAt(row, col, cell, &d.size)
			i++
		}
	}
	return finfo, fContent
}

func (c *frameCell) equalsTo(c2 *frameCell) bool {
	if string(c.chars) != string(c2.chars) {
		return false
	}
	if c.attrCode() != c2.attrCode() {
		return false
	}
	return true
}

func (d *decoderState) renderFrameTo(perv, next frameContent, out io.Writer) {
	var cursorRow, cursorCol int
	fmt.Fprintf(out, "\033[0;0H")
	var lastAttr uint64
	for row := 0; row < d.size.rows; row++ {
		for col := 0; col < d.size.cols; col++ {
			if perv != nil && perv.getCellAt(row, col, &d.size).equalsTo(next.getCellAt(row, col, &d.size)) {
				continue
			}
			if cursorRow != row || cursorCol != col {
				fmt.Fprintf(out, "\033[%d;%dH", row+1, col+1)
				cursorRow = row
				cursorCol = col
			}
			cell := next.getCellAt(row, col, &d.size)
			if row != 0 && col != 0 && cell.attrCode() == lastAttr {
				// no need to output attr
				out.Write([]byte(string(cell.chars)))
			} else {
				out.Write([]byte(cell.toOutput()))
			}
			cursorCol++
		}
	}
}
