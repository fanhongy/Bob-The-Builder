package tui

import (
	"os"
)

// KeyType represents the type of key event.
type KeyType int

const (
	KeyUp KeyType = iota
	KeyDown
	KeyLeft
	KeyRight
	KeyEnter
	KeyEscape
	KeyQ
	KeyRune
)

// KeyEvent represents a parsed keyboard input event.
type KeyEvent struct {
	Type KeyType
	Rune rune
}

// InputReader reads keyboard input in a goroutine and sends KeyEvent to a channel.
type InputReader struct {
	ch     chan KeyEvent
	done   chan struct{}
	closed bool
}

// NewInputReader creates and starts an InputReader that reads from stdin.
func NewInputReader() *InputReader {
	ir := &InputReader{
		ch:   make(chan KeyEvent, 16),
		done: make(chan struct{}),
	}
	go ir.readLoop()
	return ir
}

// Events returns the channel of key events.
func (ir *InputReader) Events() <-chan KeyEvent {
	return ir.ch
}

// Close stops the input reader goroutine.
func (ir *InputReader) Close() {
	if !ir.closed {
		ir.closed = true
		close(ir.done)
	}
}

func (ir *InputReader) readLoop() {
	buf := make([]byte, 8)
	for {
		select {
		case <-ir.done:
			return
		default:
		}

		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			continue
		}

		events := ParseInput(buf[:n])
		for _, ev := range events {
			select {
			case ir.ch <- ev:
			case <-ir.done:
				return
			}
		}
	}
}

// ParseInput parses raw bytes into key events.
// Handles escape sequences for arrow keys.
func ParseInput(data []byte) []KeyEvent {
	var events []KeyEvent
	i := 0
	for i < len(data) {
		if data[i] == 0x1b { // ESC
			if i+2 < len(data) && data[i+1] == '[' {
				switch data[i+2] {
				case 'A':
					events = append(events, KeyEvent{Type: KeyUp})
					i += 3
					continue
				case 'B':
					events = append(events, KeyEvent{Type: KeyDown})
					i += 3
					continue
				case 'C':
					events = append(events, KeyEvent{Type: KeyRight})
					i += 3
					continue
				case 'D':
					events = append(events, KeyEvent{Type: KeyLeft})
					i += 3
					continue
				}
			}
			events = append(events, KeyEvent{Type: KeyEscape})
			i++
			continue
		}

		switch data[i] {
		case '\r', '\n':
			events = append(events, KeyEvent{Type: KeyEnter})
		case 'q', 'Q':
			events = append(events, KeyEvent{Type: KeyQ})
		default:
			events = append(events, KeyEvent{Type: KeyRune, Rune: rune(data[i])})
		}
		i++
	}
	return events
}
