package main

import (
	"fmt"
	"os"
)

type options struct {
	operation string
	script    string
	timing    string
	output    string
	fps       int
	itsFile   string
	stage     struct {
		rows          int
		cols          int
		indexedColors Palettle
	}
}

const (
	opEncode string = "encode"
	opPlay   string = "play"
)

func main() {
	opt, err := parseArgs(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Argument error: %v\n", err.Error())
		os.Exit(1)
	}
	// defer (func() {
	// 	p := recover()
	// 	if p != nil {
	// 		pErr, ok := p.(error)
	// 		if !ok {
	// 			panic(p)
	// 		}
	// 		fmt.Fprintf(os.Stderr, "%v\n", pErr.Error())
	// 		os.Exit(1)
	// 	}
	// })()
	switch opt.operation {
	case opEncode:
		doOpEncode(opt)
	case opPlay:
		doOpPlay(opt)
	default:
		doOpUnknown(opt.operation)
	}
}

type Palettle [18]uint32

var (
	PalettleSolarized Palettle = Palettle{
		0x657B83, 0xFDF6E3, 0x073642, 0xDC322F, 0x859900, 0xB58900, 0x268BD2, 0xD33682, 0x2AA198, 0xEEE8D5, 0x002B36, 0xCB4B16, 0x586E75, 0x657B83, 0x839496, 0x6C71C4, 0x93A1A1, 0xFDF6E3,
	}
)

func parseArgs(args []string) (opt options, err error) {
	err = nil
	if len(args) == 4 {
		opt.operation = opEncode
		opt.stage.rows = 400
		opt.stage.cols = 300
		opt.stage.indexedColors = PalettleSolarized
		opt.script = args[1]
		opt.timing = args[2]
		opt.output = args[3]
		opt.fps = 1
	} else if len(args) == 2 {
		opt.operation = opPlay
		opt.itsFile = args[1]
	} else {
		err = fmt.Errorf("syntax: <script> <timing> <output> | <its file>")
	}
	return
}

func doOpUnknown(op string) {
	panic(fmt.Errorf("unknown operation %v", op))
}
