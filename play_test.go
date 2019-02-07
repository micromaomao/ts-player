package main

import (
	"testing"
)

func Test_decoderState_searchForFrame(t *testing.T) {
	t.Run("decoderState.searchForFrame", func(t *testing.T) {
		d := decoderState{}
		d.index = &ITSIndex{}
		d.index.Count = 11
		d.index.Frames = make([]*ITSIndex_FrameIndex, 0, d.index.Count)
		for i := uint64(0); i < d.index.Count; i++ {
			fi := &ITSIndex_FrameIndex{}
			fi.ByteOffset = i
			fi.TimeOffset = float64(i) / 10.0
			d.index.Frames = append(d.index.Frames, fi)
		}
		// d.index.Frames: [0.0, 0.1, 0.2, ..., 0.9, 1.0]
		for i := uint64(0); i <= 10; i++ {
			var toff = float64(i) / 10.0
			t.Logf("Testing for t=%v, expecting off=%v", toff, i)
			fOff, _ := d.searchForFrame(toff)
			if fOff != i {
				t.Errorf("  ... but get %v", fOff)
			}
			fOff, _ = d.searchForFrame(toff + 0.05)
			if fOff != i {
				t.Errorf("  ... but get %v when t offseted by 0.05", fOff)
			}
		}

		t.Logf("Testing for t=-2.5, expecting off=0")
		if fOff, _ := d.searchForFrame(-2.5); fOff != 0 {
			t.Errorf("  ... but got %v", fOff)
		}

		t.Logf("Testing for t=2.5, expecting off=10")
		if fOff, _ := d.searchForFrame(2.5); fOff != 10 {
			t.Errorf("  ... but got %v", fOff)
		}
	})
}
