// Copyright 2014 The gocui Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocui

import (
	"strconv"
	"sync"

	"github.com/go-errors/errors"
)

type escapeInterpreter struct {
	state                  escapeState
	curch                  rune
	csiParam               []string
	curFgColor, curBgColor Attribute
	mode                   OutputMode
	mutex                  sync.Mutex
	instruction            instruction
}

/// new algorithm: if we hit an escape key start a sequence
// if we then hit a terminator key, process the sequence

const (
	NONE = 1 << iota
	CURSOR_UP
	CURSOR_DOWN
	CURSOR_LEFT
	CURSOR_RIGHT
	CURSOR_MOVE
	CLEAR_SCREEN
	ERASE_TO_END_OF_LINE
	SAVE_CURSOR_POS
	RESTORE_CURSOR_POS
)

type instruction struct {
	kind   int
	param1 int
	param2 int
}

type escapeState int

const (
	stateNone escapeState = iota
	stateEscape
	stateCSI
	stateParams
)

var directionMap = map[rune]int{
	'A': CURSOR_UP,
	'B': CURSOR_DOWN,
	'C': CURSOR_LEFT,
	'D': CURSOR_RIGHT,
}

var (
	errNotCSI        = errors.New("Not a CSI escape sequence")
	errCSIParseError = errors.New("CSI escape sequence parsing error")
	errCSITooLong    = errors.New("CSI escape sequence is too long")
)

// runes in case of error will output the non-parsed runes as a string.
func (ei *escapeInterpreter) runes() []rune {
	ei.mutex.Lock()
	defer ei.mutex.Unlock()

	switch ei.state {
	case stateNone:
		return []rune{0x1b}
	case stateEscape:
		return []rune{0x1b, ei.curch}
	case stateCSI:
		return []rune{0x1b, '[', ei.curch}
	case stateParams:
		ret := []rune{0x1b, '['}
		for _, s := range ei.csiParam {
			ret = append(ret, []rune(s)...)
		}
		return append(ret, ei.curch)
	}
	return nil
}

// newEscapeInterpreter returns an escapeInterpreter that will be able to parse
// terminal escape sequences.
func newEscapeInterpreter(mode OutputMode) *escapeInterpreter {
	ei := &escapeInterpreter{
		state:       stateNone,
		curFgColor:  ColorDefault,
		curBgColor:  ColorDefault,
		mode:        mode,
		instruction: instruction{kind: NONE},
	}
	return ei
}

// reset sets the escapeInterpreter in initial state.
func (ei *escapeInterpreter) reset() {
	ei.mutex.Lock()
	defer ei.mutex.Unlock()

	ei.state = stateNone
	ei.curFgColor = ColorDefault
	ei.curBgColor = ColorDefault
	ei.csiParam = nil
}

func (ei *escapeInterpreter) instructionRead() {
	ei.instruction.kind = NONE
}

// parseOne parses a rune. If isEscape is true, it means that the rune is part
// of an escape sequence, and as such should not be printed verbatim. Otherwise,
// it's not an escape sequence.
func (ei *escapeInterpreter) parseOne(ch rune) (isEscape bool, err error) {
	ei.mutex.Lock()
	defer ei.mutex.Unlock()

	// Sanity checks
	if len(ei.csiParam) > 20 {
		return false, errCSITooLong
	}
	if len(ei.csiParam) > 0 && len(ei.csiParam[len(ei.csiParam)-1]) > 255 {
		return false, errCSITooLong
	}

	ei.curch = ch

	switch ei.state {
	case stateNone:
		if ch == 0x1b {
			ei.state = stateEscape
			return true, nil
		}
		return false, nil
	case stateEscape:
		if ch == '[' {
			ei.state = stateCSI
			return true, nil
		}
		return false, errNotCSI
	case stateCSI:
		switch {
		case ch >= '0' && ch <= '9':
			ei.csiParam = append(ei.csiParam, "")
		case ch == 'm':
			ei.csiParam = append(ei.csiParam, "0")
		case ch == 'K':
			ei.instruction.kind = ERASE_TO_END_OF_LINE

			ei.state = stateNone
			ei.csiParam = nil
			return true, nil
		case ch == '?':
			ei.csiParam = append(ei.csiParam, "")
		default:
			return false, errCSIParseError
		}
		ei.state = stateParams
		fallthrough
	case stateParams:
		switch {
		case (ch >= '0' && ch <= '9') || ch == '?':
			ei.csiParam[len(ei.csiParam)-1] += string(ch)
			return true, nil

		case ch == ';':
			ei.csiParam = append(ei.csiParam, "")
			return true, nil

		case ch == 'm':
			var err error
			switch ei.mode {
			case OutputNormal:
				err = ei.outputNormal()
			case Output256:
				err = ei.output256()
			}
			if err != nil {
				return false, errCSIParseError
			}

			ei.state = stateNone
			ei.csiParam = nil
			return true, nil

		case ch == 'A', ch == 'B', ch == 'C', ch == 'D':
			p, err := strconv.Atoi(ei.csiParam[0])
			if err != nil {
				return false, errCSIParseError
			}
			ei.instruction.kind = directionMap[ch]
			ei.instruction.param1 = p

			ei.state = stateNone
			ei.csiParam = nil
			return true, nil

		case ch == 'J':
			ei.instruction.kind = CLEAR_SCREEN

			ei.state = stateNone
			ei.csiParam = nil
			return true, nil

		case ch == 'h':
			if ei.csiParam[0] == "?25" {
				// wants us to show the cursor but we never hide it in the first place
			} else {
				return false, errCSIParseError
			}
			ei.state = stateNone
			ei.csiParam = nil
			return true, nil

		case ch == 'l':
			if ei.csiParam[0] == "?25" {
				// wants us to hide the cursor but we don't care
			} else {
				return false, errCSIParseError
			}
			ei.state = stateNone
			ei.csiParam = nil
			return true, nil

		default:
			return false, errCSIParseError
		}
	}
	return false, nil
}

// outputNormal provides 8 different colors:
//   black, red, green, yellow, blue, magenta, cyan, white
func (ei *escapeInterpreter) outputNormal() error {
	for _, param := range ei.csiParam {
		p, err := strconv.Atoi(param)
		if err != nil {
			return errCSIParseError
		}

		switch {
		case p >= 30 && p <= 37:
			ei.curFgColor = Attribute(p - 30 + 1)
		case p == 39:
			ei.curFgColor = ColorDefault
		case p >= 40 && p <= 47:
			ei.curBgColor = Attribute(p - 40 + 1)
		case p == 49:
			ei.curBgColor = ColorDefault
		case p == 1:
			ei.curFgColor |= AttrBold
		case p == 4:
			ei.curFgColor |= AttrUnderline
		case p == 7:
			ei.curFgColor |= AttrReverse
		case p == 0:
			ei.curFgColor = ColorDefault
			ei.curBgColor = ColorDefault
		}
	}

	return nil
}

// output256 allows you to leverage the 256-colors terminal mode:
//   0x01 - 0x08: the 8 colors as in OutputNormal
//   0x09 - 0x10: Color* | AttrBold
//   0x11 - 0xe8: 216 different colors
//   0xe9 - 0x1ff: 24 different shades of grey
func (ei *escapeInterpreter) output256() error {
	ei.mutex.Lock()
	defer ei.mutex.Unlock()

	if len(ei.csiParam) < 3 {
		return ei.outputNormal()
	}

	mode, err := strconv.Atoi(ei.csiParam[1])
	if err != nil {
		return errCSIParseError
	}
	if mode != 5 {
		return ei.outputNormal()
	}

	fgbg, err := strconv.Atoi(ei.csiParam[0])
	if err != nil {
		return errCSIParseError
	}
	color, err := strconv.Atoi(ei.csiParam[2])
	if err != nil {
		return errCSIParseError
	}

	switch fgbg {
	case 38:
		ei.curFgColor = Attribute(color + 1)

		for _, param := range ei.csiParam[3:] {
			p, err := strconv.Atoi(param)
			if err != nil {
				return errCSIParseError
			}

			switch {
			case p == 1:
				ei.curFgColor |= AttrBold
			case p == 4:
				ei.curFgColor |= AttrUnderline
			case p == 7:
				ei.curFgColor |= AttrReverse
			}
		}
	case 48:
		ei.curBgColor = Attribute(color + 1)
	default:
		return errCSIParseError
	}

	return nil
}
