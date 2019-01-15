package main

import (
	"fmt"
	"image/color"
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
		rows   int
		cols   int
		initBg color.RGBA
		initFg color.RGBA
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
	defer (func() {
		p := recover()
		if p != nil {
			pErr, ok := p.(error)
			if !ok {
				panic(p)
			}
			fmt.Fprintf(os.Stderr, "%v\n", pErr.Error())
			os.Exit(1)
		}
	})()
	switch opt.operation {
	case opEncode:
		doOpEncode(opt)
	case opPlay:
		doOpPlay(opt)
	default:
		doOpUnknown(opt.operation)
	}
}

func parseArgs(args []string) (opt options, err error) {
	err = nil
	if len(args) == 4 {
		opt.operation = opEncode
		opt.stage.rows = 200
		opt.stage.cols = 200
		opt.stage.initBg = color.RGBA{R: 253, G: 246, B: 227, A: 255}
		opt.stage.initFg = color.RGBA{R: 101, G: 123, B: 131, A: 255}
		opt.script = args[1]
		opt.timing = args[2]
		opt.output = args[3]
		opt.fps = 10
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
