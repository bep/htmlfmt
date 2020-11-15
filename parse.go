package htmlfmt

import (
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
		// newlineTracker: newlineTracker,
		Tokenizer: html.NewTokenizer(src),
	}
}

type parser struct {
	counter int

	tokens tokens

	*html.Tokenizer

	i     int
	depth int

	currType html.TokenType
	prevType html.TokenType

	tag      Tag
	tagName  []byte
	prevName []byte
}

// Want to avoid nesting of short elements such as <div><span>Hello</span></div>.
// This is hard to determine without looking ahead, so we first read the tokens
// we received from html.Tokenizer into a structure with that information.
func (tok *parser) parse() (tokens, error) {
Loop:
	for {
		tok.Next()

		var depthAdjustment int
		var inPre bool

		switch tok.currType {
		case html.StartTagToken:
			if !(inPre || isVoid(tok.tagName)) {
				depthAdjustment = 1
			}

			if !inPre && isPreformatted(tok.tagName) {
				inPre = true
			}

		case html.EndTagToken:
			isEndPre := inPre && isPreformatted(tok.tagName)
			if !isEndPre {
				depthAdjustment = -1
			} else {
				inPre = false
			}

		case html.ErrorToken:
			err := tok.Err()
			if err.Error() == "EOF" {
				break Loop
			}
			return nil, err
		}

		tok.trackOpen(depthAdjustment, inPre)

	}

	return tok.tokens, nil
}

func (tok *parser) trackOpen(depthAdjustment int, inPre bool) {
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
		inPre:    inPre,
		typ:      tok.currType,
		prevType: tok.prevType,
		raw:      raw,
		depth:    tok.depth,
		tagName:  string(tok.tagName),
		tag:      tok.tag,
		closed:   tok.currType == html.EndTagToken,
	}

	tok.counter++

	if t.closed && !t.inPre {
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

type text struct {
	b                  []byte
	hasNewline         bool
	hadLeadingNewline  bool
	hadTrailingNewline bool
	hadLeadingSpace    bool
	hadTralingSpace    bool
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
	inPre    bool
	depth    int
	children tokens
	closed   bool

	// formatter state
	indented bool
	text     *text // For text tokens
}

func (t *token) isStartIndented() bool {
	return t.startElement != nil && t.startElement.indented
}

func (t *token) isInline() bool {
	return isInline([]byte(t.tagName))
}

func (t *token) isBlock() bool {
	return !(t.isInline() || t.typ == html.TextToken)
}

func (t *token) isTextWithOnlyWhitespace() bool {
	return t.typ == html.TextToken && !nonSpaceRe.Match(t.raw)
}

func (t *token) isVoid() bool {
	return isVoid([]byte(t.tagName))
}

func (t *token) needsNewlineAppended(tabStr []byte) bool {
	if t.inPre {
		return false
	}

	if len(t.children) == 0 {
		return false
	}

	if shouldAlwaysHaveNewlineAppended([]byte(t.tagName)) {
		return true
	}

	blockCount := 0
	for _, c := range t.children {
		if c.typ == html.StartTagToken && !c.isInline() {
			blockCount++
		} else if c.typ == html.TextToken && c.text.hasNewline {
			blockCount++
		}
		if blockCount > 0 {
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

func (t *tokenIterator) Current() *token {
	tok := t.tokens[t.pos]
	return tok
}

func (t *tokenIterator) Peek() *token {
	if t.pos+1 > len(t.tokens)-1 {
		return nil
	}
	return t.tokens[t.pos+1]
}

func (t *tokenIterator) PeekStart() *token {
	i := 1
	for {
		if t.pos+i > len(t.tokens)-1 {
			return nil
		}

		tok := t.tokens[t.pos+i]
		if tok.typ == html.StartTagToken {
			return tok
		}
		i++
	}
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
	sb.WriteString("START:")
	for _, tok := range t {
		sb.WriteString(fmt.Sprintf("typ(%s)-tag(%s)-%d[depth(%d)|children(%d)|size(%d)]", tok.typ, tok.tagName, tok.i, tok.depth, len(tok.children), tok.size()))
		if tok.startElement != nil {
			sb.WriteString(fmt.Sprintf("/%d", tok.startElement.i))
		}
		sb.WriteString("///")

	}
	sb.WriteString(":END")

	return sb.String()
}

func (t *token) String() string {
	return fmt.Sprintf("%s/%s(%d)", t.tagName, t.typ, t.depth)
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

// Even for very short examples, we would not want these on one line.
func shouldAlwaysHaveNewlineAppended(tag []byte) bool {
	switch string(tag) {
	case "html", "body":
		return true
	default:
		return false
	}
}
