package htmlfmt

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

func newParser(src io.Reader, tab []byte) *parser {
	if tab == nil {
		tab = []byte("  ")
	}
	return &parser{
		tab:       tab,
		i:         -1,
		depth:     0,
		Tokenizer: html.NewTokenizer(src),
	}
}

type parser struct {
	// Configuration
	tab []byte

	// Parser state.
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
func (prs *parser) parse() (tokens, error) {
Loop:
	for {
		prs.Next()

		var depthAdjustment int
		var inPre bool

		switch prs.currType {
		case html.StartTagToken:
			if !(inPre || isVoid(string(prs.tagName))) {
				depthAdjustment = 1
			}

			if !inPre && isPreformatted(prs.tagName) {
				inPre = true
			}

		case html.EndTagToken:
			isEndPre := inPre && isPreformatted(prs.tagName)
			if !isEndPre {
				depthAdjustment = -1
			} else {
				inPre = false
			}

		case html.ErrorToken:
			err := prs.Err()
			if err.Error() == "EOF" {
				break Loop
			}
			return nil, err
		}

		prs.trackOpen(depthAdjustment, inPre)

	}

	return prs.tokens, nil
}

func (prs *parser) trackOpen(depthAdjustment int, inPre bool) {
	raw := make([]byte, len(prs.Raw()))
	copy(raw, prs.Raw())

	t := &token{
		i:        prs.counter,
		inPre:    inPre,
		typ:      prs.currType,
		prevType: prs.prevType,
		raw:      raw,
		tag:      prs.tag,
		closed:   prs.currType == html.EndTagToken,
	}

	switch prs.currType {
	case html.EndTagToken:
		prs.depth += depthAdjustment
	case html.TextToken:
		t.text = prepareText(t.raw, prs.tab)
		fallthrough
	default:
		defer func() {
			prs.depth += depthAdjustment
		}()
	}

	t.depth = prs.depth
	prs.counter++

	if t.closed && !t.inPre {
		// Attach the start element to the end, if possible.
		for i := len(prs.tokens) - 1; i >= 0; i-- {
			tt := prs.tokens[i]

			if tt.closed || t.tag.Name != tt.tag.Name || t.typ <= tt.typ || tt.typ == html.TextToken {
				continue
			}

			if tt != t && tt.depth == t.depth && t.tag.Name == tt.tag.Name {
				t.startElement = tt
				tt.closed = t.closed
				break
			}
		}
	}

	for i := len(prs.tokens) - 1; i >= 0; i-- {
		tt := prs.tokens[i]

		if tt == t || tt.closed {
			continue
		}

		if (t.depth - tt.depth) == 1 {
			tt.children = append(tt.children, t)
			break
		}
	}

	prs.tokens = append(prs.tokens, t)
}

type text struct {
	b                  []byte
	hasNewline         bool
	isWhitespaceOnly   bool
	hadLeadingNewline  bool
	hadTrailingNewline bool
	hadLeadingSpace    bool
	hadTralingSpace    bool
}

func (t text) IsZero() bool {
	return t.b == nil
}

type token struct {
	i int

	// From html.Tokenizer
	typ      html.TokenType
	prevType html.TokenType
	raw      []byte

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
	text     text // For text tokens
}

func (t *token) isStartIndented() bool {
	return t.startElement != nil && t.startElement.indented
}

func (t *token) isInline() bool {
	return isInline(t.tag.Name)
}

func (t *token) isBlock() bool {
	return !(t.isInline() || t.typ == html.TextToken)
}

func (t *token) isVoid() bool {
	return isVoid(t.tag.Name)
}

func (t *token) needsNewlineAppended() bool {
	if t.inPre {
		return false
	}

	if len(t.children) == 0 {
		return false
	}

	if shouldAlwaysHaveNewlineAppended(t.tag.Name) {
		return true
	}

	blockCount := 0
	for _, c := range t.children {
		if c.typ == html.StartTagToken && !c.isInline() {
			blockCount++
		} else if c.text.hasNewline {
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
		if !t.text.isWhitespaceOnly {
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
		sb.WriteString(fmt.Sprintf("typ(%s)-tag(%s)-%d[depth(%d)|children(%d)|size(%d)]", tok.typ, tok.tag.Name, tok.i, tok.depth, len(tok.children), tok.size()))
		if tok.startElement != nil {
			sb.WriteString(fmt.Sprintf("/%d", tok.startElement.i))
		}
		sb.WriteString("///")

	}
	sb.WriteString(":END")

	return sb.String()
}

func (t *token) String() string {
	return fmt.Sprintf("%s/%s(%d)", t.tag.Name, t.typ, t.depth)
}

func isInline(tag string) bool {
	switch tag {
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

func isVoid(tag string) bool {
	switch string(tag) {
	case "input", "link", "meta", "hr", "img", "br", "area", "base", "col",
		"param", "command", "embed", "keygen", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

// Even for very short examples, we would not want these on one line.
func shouldAlwaysHaveNewlineAppended(tag string) bool {
	switch tag {
	case "html", "body":
		return true
	default:
		return false
	}
}
