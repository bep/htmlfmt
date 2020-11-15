package htmlfmt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"sync"
	"unicode"

	"golang.org/x/net/html"
)

var (
	leadingSpaceRe    = regexp.MustCompile(`^\s+\S`)
	trailingSpaceRe   = regexp.MustCompile(`\S\s+$`)
	leadingNewlineRe  = regexp.MustCompile(`^\s*\r?\n`)
	trailingNewlineRe = regexp.MustCompile(`\n\s*$`)
	nonSpaceRe        = regexp.MustCompile(`\S`)
)

// Elements with length in bytes above this threshold will be wrapped
// and indented. This includes the start/end tags.
// This allows short blocks such as <div>Hi</div> to be kept on one line.
const sizeNewlineThreshold = 30

// New HTML formatter.
// Can be safely reused.
func New(options ...Option) *Formatter {
	f := &Formatter{
		tabStr:  []byte("  "),
		newline: []byte("\n"),
		textFormatters: func(tag Tag) TextFormatter {
			return nil
		},
	}
	for _, option := range options {
		option(f)
	}
	return f
}

// WithNewlineAttributePlaceholder is admittedly a specialist option.
//
// It was added for the gotfmt Go template preprocessor which needed
// to preserve some newlines around certain template blocks.
// Setting this to "newline" and the formatter will only print the whitespace
// produced by "<br newline/>".
//
// Note that this is only supported for void/self closing elements.
func WithNewlineAttributePlaceholder(attribute string) Option {
	return func(f *Formatter) { f.newlineAttributePlaceholder = attribute }
}

// WithTab configures the formatter use tab as indentation.
func WithTab(tab string) Option { return func(f *Formatter) { f.tabStr = []byte(tab) } }

// WithTextFormatters configures the formatter to use the provided lookup
// func to find a formatter for a block of text inside tag (e.g. a JavaScript formatter).
func WithTextFormatters(lookup func(tag Tag) TextFormatter) Option {
	return func(f *Formatter) { f.textFormatters = lookup }
}

// Attribute represents an HTML attribute.
type Attribute struct {
	Key   string
	Value string
}

// IsZero returns whenter a is zero.
func (a Attribute) IsZero() bool {
	return a.Key == ""
}

// Attributes is a slice of Attribute.
type Attributes []Attribute

// ByKey finds an Attribute by its key.
// Returns a zero value if not found.
func (a Attributes) ByKey(key string) Attribute {
	for _, attr := range a {
		if attr.Key == key {
			return attr
		}
	}
	return Attribute{}
}

// Formatter configures the formatting and can be safely reused.
type Formatter struct {
	// options
	tabStr                      []byte
	newline                     []byte
	textFormatters              func(tag Tag) TextFormatter
	newlineAttributePlaceholder string
}

// Format formats src and writes the result to dst.
func (f *Formatter) Format(dst io.Writer, src io.Reader) error {
	p := newParser(src)

	tokens, err := p.parse()
	if err != nil {
		return err
	}

	for _, tok := range tokens {
		if tok.typ == html.TextToken {
			tok.text = prepareText(tok.raw, f.tabStr)
		}
	}

	iter := &tokenIterator{
		tokens: tokens,
		pos:    -1,
	}

	w := &writer{
		dst:         dst,
		f:           f,
		iter:        iter,
		enableDebug: false,
	}

	var formatText TextFormatter = nil
	var inPre bool

	for {
		curr := iter.Next()
		if curr == nil {
			break
		}

		if inPre && !curr.inPre {
			w.write(curr.raw)
			continue
		}

		prev := iter.Prev()
		next := iter.Peek()

		if curr.typ == html.TextToken && !nonSpaceRe.Match(curr.raw) {
			// Whitespace only.
			if prev == nil && leadingNewlineRe.Match(curr.raw) {
				// Preserve one leading newline.
				w.newline()
			}

			if next == nil && trailingNewlineRe.Match(curr.raw) {
				// Preserve one trailing newline.
				w.newline()
			}

			if prev == nil || !prev.inPre {
				// Nothing more to do.
				continue
			}
		}

		var newlineAttribute bool
		if curr.typ != html.TextToken && f.newlineAttributePlaceholder != "" {
			newlineAttribute = !curr.tag.Attributes.ByKey(f.newlineAttributePlaceholder).IsZero()
			if newlineAttribute && !curr.isVoid() {
				return errors.New("newline attributes is for void attributes only")
			}
		}

		if newlineAttribute {
			// Insert newline only.
			w.newlineForced()
			continue
		}

		switch curr.typ {
		case html.StartTagToken:
			if curr.inPre {
				inPre = true
				w.write(curr.raw)
				continue
			}

			// A text formatter for e.g. JavaScript script tags currently assumes
			// a single wrapped text element and any whitespace handling is
			// delegated to the custom text formatter.
			formatText = f.textFormatters(curr.tag)

			var needsNewlineAppended bool

			if formatText == nil {
				needsNewlineAppended = curr.needsNewlineAppended(f.tabStr)
				if needsNewlineAppended {
					curr.indented = true
					w.depth++
					w.debug("depth.incr")
				} else if prev != nil && next != nil && curr.isVoid() {
					if w.newline() {
						w.tab()
					}
				}
			}

			w.write(curr.raw)

			if formatText == nil {
				if needsNewlineAppended || (prev != nil && next != nil && curr.isVoid()) {
					if w.newline() {
						w.tab()
					}
				}
			}
		case html.SelfClosingTagToken, html.CommentToken, html.DoctypeToken:
			w.write(curr.raw)
			if prev == nil && next != nil {
				w.newline()
			}
		case html.EndTagToken:
			if curr.inPre {
				inPre = false
				w.write(curr.raw)
				continue
			}
			if formatText == nil {
				if curr.isStartIndented() {
					n := w.newline()
					w.depth--
					w.debug("depth.decr")
					if w.depth < 0 {
						w.depth = 0
					}
					if n {
						w.tab()
					}
				}
			}

			w.write(curr.raw)

			if next != nil && !next.isInline() {
				nextStart := iter.PeekStart()
				if nextStart != nil && curr.depth == nextStart.depth {
					if w.newline() {
						w.tab()
					}
				}
			}
		case html.TextToken:
			if prev != nil && (prev.typ == html.EndTagToken || prev.isVoid()) && prev.isBlock() {
				if w.newline() {
					w.tab()
				}
			}

			// Preserve one leading newline.
			if prev == nil && curr.text.hadLeadingNewline {
				if w.newline() {
					w.tab()
				}
			}

			if formatText != nil {
				w.write(formatText(curr.raw, w.depth))
			} else {
				w.handleTextToken(prev, curr, next)
			}

			// Preserve one trailing newline.
			if next != nil && !next.isInline() && curr.text.hadTrailingNewline && next.depth == curr.depth {
				if w.newline() {
					w.tab()
				}
			}
		default:
			panic("Unhandled token")
		}
	}

	return nil
}

// Option sets an option of the HTML formatter.
type Option func(f *Formatter)

// Tag represents a HTML tag.
type Tag struct {
	Name       string
	Attributes Attributes
}

// IsZero returns whether t is zero.
func (t Tag) IsZero() bool {
	return t.Name == ""
}

// TextFormatter allows clients to plug in a text formatter for a given
// tag, e.g. <script> blocks.
type TextFormatter func(text []byte, depth int) []byte

func (tok *parser) Next() html.TokenType {
	typ := tok.Tokenizer.Next()

	// i is initialized at -1
	tok.i++
	if tok.i > 0 {
		tok.prevType = tok.currType
	}
	tok.currType = typ

	var hasAttrs bool
	if tok.currType != html.TextToken {
		tok.prevName = tok.tagName
		tok.tagName, hasAttrs = tok.TagName()
		tok.tag = Tag{
			Name: string(tok.tagName),
		}
	}

	if hasAttrs {
		for {
			key, val, more := tok.TagAttr()
			tok.tag.Attributes = append(tok.tag.Attributes, Attribute{
				Key:   string(key),
				Value: string(val),
			})

			if !more {
				break
			}
		}
	}

	return tok.currType
}

type writer struct {
	dst  io.Writer
	f    *Formatter
	iter *tokenIterator

	// For development.
	enableDebug bool

	depth        int // TODO1 usage
	newlineDepth int
}

func prepareText(inTxt, tabStr []byte) *text {
	txt := bytes.Replace(inTxt, []byte{'\t'}, tabStr, -1)
	txt = bytes.Trim(txt, " \r\n")
	hasNewline := bytes.Contains(txt, []byte{'\n'})

	return &text{
		b:                  txt,
		hasNewline:         hasNewline,
		hadLeadingNewline:  leadingNewlineRe.Match(inTxt),
		hadTrailingNewline: trailingNewlineRe.Match(inTxt),
		hadLeadingSpace:    leadingSpaceRe.Match(inTxt),
		hadTralingSpace:    trailingSpaceRe.Match(inTxt),
	}
}

func (w *writer) defaultTextTokenHandler(prev, curr, next *token) {
	txt := curr.text
	text := txt.b

	if txt.hadTralingSpace && next != nil && next.typ == html.StartTagToken && next.isInline() {
		text = append(text, ' ')
	}

	if text == nil {
		return
	}

	prevIsInlineEndTag := prev != nil && prev.typ == html.EndTagToken && prev.isInline()
	if txt.hasNewline {
		if prevIsInlineEndTag {
			if txt.hadLeadingSpace {
				text = append([]byte{' '}, text...)
			}
		}
		w.write(w.formatText(text))
	} else {
		if prevIsInlineEndTag && txt.hadLeadingSpace {
			text = append([]byte{' '}, text...)
		}
		w.write(text)
	}
}

func (w *writer) handleTextToken(prev, curr, next *token) {
	if curr.inPre {
		w.write(curr.raw)
	} else {
		w.defaultTextTokenHandler(prev, curr, next)
	}
}

func (w *writer) debug(what string) {
	if w.enableDebug {
		curr := w.iter.Current()
		fmt.Printf("%s(%s/%s)(%d/%d)\n", what, curr.tagName, curr.typ, w.depth, w.newlineDepth)
	}
}

var regexpCache = struct {
	sync.RWMutex
	m map[string]*regexp.Regexp
}{m: make(map[string]*regexp.Regexp)}

func getOrCompileRegexp(re string) *regexp.Regexp {
	regexpCache.RLock()
	if r, ok := regexpCache.m[re]; ok {
		regexpCache.RUnlock()
		return r
	}
	regexpCache.RUnlock()

	regexpCache.Lock()
	r := regexp.MustCompile(re)
	regexpCache.m[re] = r
	regexpCache.Unlock()

	return r
}

func (w *writer) formatText(txt []byte) []byte {
	return formatTextBlock(w.f.tabStr, txt, w.depth)
}

func formatTextBlock(tabStr, txt []byte, depth int) []byte {
	var (
		idx        = -1
		isNotSpace = func(r rune) bool { return !unicode.IsSpace(r) }
	)

	parts := bytes.Split(txt, []byte("\n "))
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		i := bytes.IndexFunc(part, isNotSpace)

		if idx == -1 || (i != -1 && i < idx) {
			idx = i + 1
		}
	}

	if idx < 1 {
		idx = 0
	}

	re := getOrCompileRegexp(`\n\s{` + strconv.Itoa(idx) + `}`)
	return re.ReplaceAllLiteral(txt, append([]byte{'\n'}, bytes.Repeat(tabStr, depth)...))
}

func (w *writer) newline() bool {
	w.newlineDepth++
	if w.newlineDepth > 1 {
		return false
	}
	w.debug("newline")
	w.mustWrite(w.f.newline)
	return true
}

func (w *writer) newlineForced() {
	w.debug("newlineForced")
	w.mustWrite(w.f.newline)
}

func (w *writer) tab() {
	w.debug(fmt.Sprintf("tab(%d)", w.depth))
	w.mustWrite(bytes.Repeat(w.f.tabStr, w.depth))
}

func (w *writer) write(p []byte) bool {
	if w.enableDebug {
		if nonSpaceRe.Match(p) {
			w.debug(fmt.Sprintf("write(%s)", p))
		}
	}
	w.newlineDepth = 0
	w.mustWrite(p)
	return true
}

func (w *writer) mustWrite(p []byte) {
	_, err := w.dst.Write(p)
	if err != nil {
		panic(err)
	}
}
