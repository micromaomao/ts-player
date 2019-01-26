package main

import (
	"encoding/binary"
	"fmt"
	"github.com/dustin/go-humanize"
	"github.com/golang/protobuf/proto"
	"github.com/valyala/gozstd"
	"io"
	"math"
	"os"
)

func doOpOptimize(opt options) {
	fIts, err := os.OpenFile(opt.itsFile, os.O_RDONLY, 0)
	if err != nil {
		panic(err)
	}
	fIts.Seek(int64(len(FileMagic)), os.SEEK_CUR)
	var headerLen uint32
	binary.Read(fIts, binary.BigEndian, &headerLen)
	if headerLen > 10000 || headerLen < 1 {
		panic("Invalid headerLen")
	}
	headerBuff := make([]byte, headerLen)
	n, err := fIts.Read(headerBuff)
	if uint32(n) < headerLen || err == io.EOF {
		panic("Permature EOF")
	}
	header := &ITSHeader{}
	err = proto.Unmarshal(headerBuff, header)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err.Error())
		header.Version = 1
		header.FirstFrameOffset = uint64(len(FileMagic)) + 4 + uint64(headerLen)
		header.Rows = 400
		header.Cols = 300
		header.CompressionMode = ITSHeader_COMPRESSION_ZSTD
		header.CompressionDict = []byte{}
		header.IndexOffset = 1 << 63
		fmt.Fprintf(os.Stderr, "Invalid/damaged header. Assuming rows=%v, cols=%v recording buffer, frame content compressed without dict.\n", header.GetRows(), header.GetCols())
	} else if header.GetIndexOffset() <= 0 {
		header.IndexOffset = 1 << 63
	}
	if header.GetVersion() != 1 {
		panic("Invalid file version. Please update this player.")
	}
	d := &decoderState{}
	d.frameSize.rows = int(header.GetRows())
	d.frameSize.cols = int(header.GetCols())
	if d.frameSize.rows*d.frameSize.cols <= 0 {
		panic("Invalid dimension")
	}
	switch header.GetCompressionMode() {
	case ITSHeader_COMPRESSION_ZSTD:
		d.compressed = true
		compressedDictBuf := header.GetCompressionDict()
		if len(compressedDictBuf) > 0 {
			dictBuf, err := gozstd.Decompress(nil, compressedDictBuf)
			if err != nil {
				panic(err)
			}
			d.ddict, err = gozstd.NewDDict(dictBuf)
			if err != nil {
				panic(err)
			}
		} else {
			d.ddict = nil
		}
	case ITSHeader_COMPRESSION_NONE:
		d.compressed = false
	default:
		panic("Unknown compression mode")
	}
	d.file = fIts
	fOut, err := os.OpenFile(opt.output, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(fmt.Errorf("%v when opening %v for writing", err.Error(), opt.output))
	}
	err = fOut.Truncate(0)
	if err != nil {
		panic(err)
	}

	// pass one: count frames
	firstOffset := header.GetFirstFrameOffset()
	stat, err := fIts.Stat()
	var fileSize uint64
	if err == nil {
		fileSize = uint64(stat.Size())
	}
	nextOffset := firstOffset
	inputFrameIndex := &ITSIndex{}
	inputFrameIndex.Count = 0
	inputFrameIndex.Frames = make([]*ITSIndex_FrameIndex, 0, 1000)
	for {
		var err error
		var frameStruct *ITSFrame
		thisOffset := nextOffset
		if thisOffset >= header.IndexOffset {
			break
		}
		frameStruct, nextOffset, err = d.readFrameStructFromOffset(nextOffset)
		if err != nil {
			break
		}
		inputFrameIndex.Count++
		fIndexEntry := &ITSIndex_FrameIndex{}
		fIndexEntry.TimeOffset = frameStruct.GetTimeOffset()
		fIndexEntry.ByteOffset = thisOffset
		inputFrameIndex.Frames = append(inputFrameIndex.Frames, fIndexEntry)
		if inputFrameIndex.Count%10 == 0 {
			fmt.Fprintf(os.Stderr, "\r\033[2KIndexing frames... (%v / %v)", humanize.Bytes(firstOffset+thisOffset), humanize.Bytes(fileSize))
		}
	}
	fmt.Fprintf(os.Stderr, "\r\033[2KThere are %v frames.\n", inputFrameIndex.Count)
	d.index = inputFrameIndex
	const numSamples = 1000
	dictSamples := make([][]byte, 0, numSamples)
	skip := inputFrameIndex.Count / numSamples
	if skip < 1 {
		skip = 1
	}
	var i uint64
	for ; i < inputFrameIndex.Count; i += skip {
		off := inputFrameIndex.Frames[i].ByteOffset
		frameData, _, err := d.readFrameBytesFromOffset(off)
		if err != nil {
			panic(err)
		}
		dictSamples = append(dictSamples, frameData)
		if len(dictSamples)%200 == 0 {
			fmt.Fprintf(os.Stderr, "\r\033[2KBuilding compression dict... (%v / %v)", i, inputFrameIndex.Count/skip)
		}
	}
	fmt.Fprintf(os.Stderr, "\r\033[2KFinalizing compression dict...")
	dict := gozstd.BuildDict(dictSamples, len(dictSamples)*20)
	fmt.Fprintf(os.Stderr, "\n")

	e := &encoderState{}
	fOut.Seek(0, os.SEEK_SET)
	e.t = nil
	e.size = d.frameSize
	e.dict = dict
	e.cdict, err = gozstd.NewCDict(dict)
	if err != nil {
		panic(err)
	}
	e.initOutputFile(fOut)
	lastIndexEntry := inputFrameIndex.Frames[len(inputFrameIndex.Frames)-1]
	lastFrame, _, err := d.readFrameStructFromOffset(lastIndexEntry.ByteOffset)
	if err != nil {
		panic(err)
	}
	totalTime := math.Round((lastFrame.GetTimeOffset()+lastFrame.GetDuration())*10) / 10
	for i := uint64(0); i < inputFrameIndex.Count; i++ {
		bOff := inputFrameIndex.Frames[i].ByteOffset
		finfo, content, err, _ := d.readFrameFromOffset(bOff)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError reading frame %v\n", i)
			continue
		}
		e.writeFrame(&finfo, content)
		if i%5 == 0 {
			fmt.Fprintf(os.Stderr, "\r\033[2KWritting frame %v / %v, t=%vs / %vs", i, inputFrameIndex.Count, math.Round(finfo.time*10)/10, totalTime)
		}
	}
	fmt.Fprintf(os.Stderr, "\r\033[2KWrote %v frames, finalizing file...\n", inputFrameIndex.Count)
	e.finalize()
	fOut.Close()
}
