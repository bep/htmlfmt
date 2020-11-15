package htmlfmt

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strconv"
	"sync"
	"unicode"

	"golang.org/x/net/html"
)

var (
	leadingSpaceRe  = regexp.MustCompile(`^\s+\S`)
	trailingSpaceRe = regexp.MustCompile(`\S\s+$`)
	nonSpaceRe      = regexp.MustCompile(`\S`)
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
// It was added for the gotfmt Go template preprocessor which needed
// to preserve some newlines around certain template blocks.
// Setting this to "newline" and the formatter will only print the whitespace
// produced by "<br newline/>".
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

	iter := &tokenIterator{
		tokens: tokens,
		pos:    -1,
	}

	w := &writer{
		dst: dst,
		f:   f,
	}

	var formatText TextFormatter = nil

	for {
		curr := iter.Next()
		if curr == nil {
			break
		}

		prev := iter.Prev()
		next := iter.Peek()

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
			// A text formatter for e.g. JavaScript script tags currently assumes
			// a single wrapped text element and any whitespace handling is
			// delegated to the custom text formatter.
			formatText = f.textFormatters(curr.tag)

			if formatText == nil {
				if prev != nil && curr.isVoid() {
					w.newline()
				}

				if prev != nil && !prev.isInline() {
					w.tab()
				}
			}

			w.write(curr.raw)

			if formatText == nil {
				var addNewline bool
				if next != nil && curr.isVoid() {
					addNewline = true
				} else if curr.needsNewlineAppended() {
					addNewline = true
					curr.indented = true
					w.depth++
				}

				if addNewline {
					w.newline()
				}
			}

		case html.SelfClosingTagToken, html.CommentToken, html.DoctypeToken:
			if prev != nil {
				w.newline()
			}
			w.write(curr.raw)

		case html.EndTagToken:
			if formatText == nil && curr.startElement != nil {
				if curr.startElement.indented {
					w.newline()
					w.depth--
					if w.depth < 0 {
						w.depth = 0
					}
					w.tab()
				}
			}

			w.write(curr.raw)

			if !curr.isPre && next != nil && !curr.isInline() {
				w.newline()
			}
		case html.TextToken:
			// Preserve one leading newline.
			if prev == nil && bytes.HasPrefix(curr.raw, []byte("\n")) {
				w.newline()
				curr.raw = curr.raw[1:]
			}

			if formatText != nil {
				w.write(formatText(curr.raw, w.depth))
			} else {
				if prev != nil && prev.indented {
					w.tab()
				}
				w.handleTextToken(prev, curr, next)
			}

			if prev != nil {
				// Preserve one trailing newline.
				if next == nil && bytes.HasSuffix(curr.raw, []byte("\n")) {
					w.newline()
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
	dst io.Writer
	f   *Formatter

	newlineDepth int
	lastWasTab   bool

	depth int
}

func (w *writer) defaultTextTokenHandler(prev, curr, next *token) {
	t := bytes.Replace(curr.raw, []byte{'\t'}, w.f.tabStr, -1)

	text := bytes.Trim(t, "\n\r ")

	if trailingSpaceRe.Match(t) && next != nil && next.typ == html.StartTagToken && next.isInline() {
		text = append(text, ' ')
	}

	if text == nil {
		return
	}

	prevIsInlineEndTag := prev != nil && prev.typ == html.EndTagToken && prev.isInline()
	if bytes.Contains(text, []byte{'\n'}) {
		if prevIsInlineEndTag {
			if leadingSpaceRe.Match(t) {
				text = append([]byte{' '}, text...)
			}
		}
		w.write(w.formatText(text))
	} else {
		if prevIsInlineEndTag && leadingSpaceRe.Match(t) {
			text = append([]byte{' '}, text...)
		}
		w.write(text)
	}
}

func (w *writer) handleTextToken(prev, curr, next *token) {
	if curr.isPre {
		w.write(curr.raw)
	} else {
		w.defaultTextTokenHandler(prev, curr, next)
	}
}

func (w *writer) newline() {
	w.write(w.f.newline)
}

func (w *writer) newlineForced() {
	w.writeForced(w.f.newline)
}

func (w *writer) tab() {
	// Invoking tab() twice without any other writes in-between is always wrong
	// and it's easier to keep track of that here.
	if w.lastWasTab {
		return
	}
	w.write(bytes.Repeat(w.f.tabStr, w.depth))
	w.lastWasTab = true
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
	var (
		idx        = -1
		isNotSpace = func(r rune) bool { return !unicode.IsSpace(r) }
	)

	parts := bytes.Split(txt, []byte("\n "))
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		i := bytes.IndexFunc(part, isNotSpace)
		if idx == -1 || (i != -1 && i < idx) {
			idx = i
		}
	}

	var re = getOrCompileRegexp(`\n\s{` + strconv.Itoa(idx+1) + `}`)
	return re.ReplaceAllLiteral(txt, append([]byte{'\n'}, bytes.Repeat(w.f.tabStr, w.depth)...))
}

func (w *writer) write(p []byte) {
	isNewline := bytes.Equal(p, w.f.newline)
	w.lastWasTab = false
	if isNewline {
		w.newlineDepth++
		if w.newlineDepth > 1 {
			return
		}
	} else if bytes.TrimSpace(p) != nil {
		w.newlineDepth = 0
	}
	w.writeForced(p)

}

func (w *writer) writeForced(p []byte) {
	_, err := w.dst.Write(p)
	if err != nil {
		panic(err)
	}
}
