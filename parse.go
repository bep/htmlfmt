package htmlfmt

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

func newParser(src io.Reader) *parser {
	return &parser{
		//f:              f,
		i:     -1,
		depth: 0,
		//newlineTracker: newlineTracker,
		Tokenizer: html.NewTokenizer(src),
	}

}

type parser struct {
	counter int

	tokens tokens

	*html.Tokenizer

	i        int
	depth    int
	currType html.TokenType
	prevType html.TokenType
	tagName  []byte
	tag      Tag
	prevName  []byte
	preTag   []byte
}

// Want to avoid nesting of short elements such as <div><span>Hello</span></div>.
// This is hard to determine without looking ahead, so we first read the tokens
// we received from html.Tokenizer into a structure with that information.
func (tok *parser) parse() (tokens, error) {

Loop:
	for {
		tok.Next()

		var depthAdjustment int

		switch tok.currType {
		case html.StartTagToken:
			if !(tok.preTag != nil || isVoid(tok.tagName)) {
				depthAdjustment = 1
			}

			if isPreformatted(tok.tagName) {
				tok.preTag = tok.tagName
			}

		case html.EndTagToken:
			inPreTag := bytes.Equal(tok.tagName, tok.preTag)
			if !inPreTag {
				depthAdjustment = -1
			}
			if inPreTag {
				tok.preTag = nil
			}
		case html.ErrorToken:
			err := tok.Err()
			if err.Error() == "EOF" {
				break Loop
			}
			return nil, err
		}

		tok.trackOpen(depthAdjustment, tok.preTag != nil)

	}

	return tok.tokens, nil
}

func (tok *parser) trackOpen(depthAdjustment int, isPre bool) {
	if tok.currType == html.ErrorToken {
		return
	}

	if tok.currType == html.EndTagToken {
		tok.depth += depthAdjustment
	} else {
		defer func() {
			tok.depth += depthAdjustment
		}()
	}

	raw := make([]byte, len(tok.Raw()))
	copy(raw, tok.Raw())

	t := &token{
		i:        tok.counter,
		isPre:    isPre,
		typ:      tok.currType,
		prevType: tok.prevType,
		raw:      raw,
		depth:    tok.depth,
		tagName:  string(tok.tagName),
		tag:      tok.tag,
		closed:   tok.currType == html.EndTagToken,
	}

	tok.counter++

	if t.closed && !t.isPre {
		// Attach the start element to the end, if possible.
		for i := len(tok.tokens) - 1; i >= 0; i-- {
			tt := tok.tokens[i]

			if tt.closed || t.tagName != tt.tagName || t.typ <= tt.typ || tt.typ == html.TextToken {
				continue
			}

			if tt != t && tt.depth == t.depth && t.tagName == tt.tagName {
				t.startElement = tt
				tt.closed = t.closed
				break
			}
		}
	}

	for i := len(tok.tokens) - 1; i >= 0; i-- {
		tt := tok.tokens[i]

		if tt == t || tt.closed {
			continue
		}

		if (t.depth - tt.depth) == 1 {
			tt.children = append(tt.children, t)
			break
		}
	}

	tok.tokens = append(tok.tokens, t)

}

type token struct {
	i int

	// From html.Tokenizer
	typ      html.TokenType
	prevType html.TokenType
	raw      []byte
	tagName  string

	tag Tag

	startElement *token

	sizeBytes     int
	sizeBytesInit sync.Once

	// parser state
	isPre    bool
	depth    int
	children tokens
	closed   bool

	// formatter state
	indented bool
}

func (t *token) isInline() bool {
	return isInline([]byte(t.tagName))
}

func (t *token) isTextWithOnlyWhitespace() bool {
	return t.typ == html.TextToken && !nonSpaceRe.Match(t.raw)
}

func (t *token) isVoid() bool {
	return isVoid([]byte(t.tagName))
}

func (t *token) needsNewlineAppended() bool {
	if t.isPre {
		return false
	}

	if len(t.children) == 0 {
		return false
	}

	blockCount := 0
	for _, c := range t.children {

		if c.typ == html.StartTagToken && !c.isInline() {
			blockCount++
		}
		if blockCount > 1 {
			return true
		}
	}

	return t.size() > sizeNewlineThreshold
}

// size() returns the size in bytes of itself and all its descendants.
// The value of size() is cached, so don't use it until the document is fully
// parsed.
func (t *token) size() int {
	t.sizeBytesInit.Do(func() {
		s := 0
		if !t.isTextWithOnlyWhitespace() {
			s = len(t.raw)
		}

		for _, tt := range t.children {
			s += tt.size()
		}

		t.sizeBytes = s
	})

	return t.sizeBytes
}

type tokenIterator struct {
	pos    int
	tokens []*token
}

func (t *tokenIterator) Next() *token {
	t.pos++
	if len(t.tokens) <= t.pos {
		return nil
	}
	tok := t.tokens[t.pos]
	return tok
}

func (t *tokenIterator) Peek() *token {
	if t.pos+1 > len(t.tokens)-1 {
		return nil
	}
	return t.tokens[t.pos+1]
}

func (t *tokenIterator) Prev() *token {
	if t.pos <= 0 {
		return nil
	}
	return t.tokens[t.pos-1]
}

type tokens []*token

// Used in tests.
func (t tokens) String() string {
	var sb strings.Builder

	for _, tok := range t {
		sb.WriteString(fmt.Sprintf("%s-%s-%d[%d:%d:%d]", tok.typ, tok.tagName, tok.i, tok.depth, len(tok.children), tok.size()))
		if tok.startElement != nil {
			sb.WriteString(fmt.Sprintf("/%d", tok.startElement.i))
		}
		sb.WriteString("|")

	}

	return sb.String()
}

func isInline(tag []byte) bool {
	switch string(tag) {
	case "a", "b", "i", "em", "strong", "code", "span", "ins",
		"big", "small", "tt", "abbr", "acronym", "cite", "dfn",
		"kbd", "samp", "var", "bdo", "map", "q", "sub", "sup":
		return true
	default:
		return false
	}
}

func isPreformatted(tag []byte) bool {
	switch string(tag) {
	case "pre", "textarea", "code":
		return true
	default:
		return false
	}
}

func isVoid(tag []byte) bool {
	switch string(tag) {
	case "input", "link", "meta", "hr", "img", "br", "area", "base", "col",
		"param", "command", "embed", "keygen", "source", "track", "wbr":
		return true
	default:
		return false
	}
}
