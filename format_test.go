package htmlfmt

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"strings"
	"testing"

	"github.com/yosssi/gohtml"

	qt "github.com/frankban/quicktest"
)

var (
	longTextWithoutNewlines = strings.Repeat("a bba", 12)
	longTextWithNewlines    = strings.Repeat(strings.Repeat("a bba", 5)+"\n", 3)
)

func TestFormat(t *testing.T) {
	c := qt.New(t)

	formatAndCheck := func(c *qt.C, numIterations int, unformatted string, expect interface{}, options ...Option) {
		for i := 0; i < numIterations; i++ {
			f := New(options...)
			var b bytes.Buffer

			err := f.Format(&b, strings.NewReader(unformatted))

			shouldFail, ok := expect.(bool)
			if ok {
				if shouldFail {
					c.Assert(err, qt.Not(qt.IsNil))
				} else {
					c.Assert(err, qt.IsNil)
				}
				return
			}

			c.Assert(err, qt.IsNil)

			actual := b.String()
			formatted := expect.(string)

			c.Assert(actual, qt.Equals, formatted, qt.Commentf("[%d]\n%s", i, actual+"\n____\nexpected:\n"+formatted))

			// Make sure we can repeat the process and get the same result.
			unformatted = formatted
		}
	}

	c.Run("Basic", func(c *qt.C) {
		formatAndCheck(c, 2, "<div><div>Hello</div><div>World</div></div>", "<div>\n  <div>Hello</div>\n  <div>World</div>\n</div>")
		formatAndCheck(c, 2, "<div><div>Hello</div><div><span>s1</span><span>s2</span></div></div>", "<div>\n  <div>Hello</div>\n  <div>\n    <span>s1</span><span>s2</span>\n  </div>\n</div>")
		formatAndCheck(c, 2, fmt.Sprintf("<div class=\"%s\"></div>", longTextWithoutNewlines), fmt.Sprintf("<div class=\"%s\"></div>", longTextWithoutNewlines))
	})

	c.Run("Preserve newlines at both ends", func(c *qt.C) {
		formatAndCheck(c, 2, "\n<div>Hello</div>\n", "\n<div>Hello</div>\n")
		formatAndCheck(c, 2, "\n\n\n<div>Hello</div>\n\n\n\n", "\n<div>Hello</div>\n")
		formatAndCheck(c, 2, "<div>Hello</div>\n", "<div>Hello</div>\n")
		formatAndCheck(c, 2, "<div>Hello</div>", "<div>Hello</div>")
	})

	c.Run("Newline attribute placeholder", func(c *qt.C) {
		opt := WithNewlineAttributePlaceholder("newline")
		// Should fail. Void elements only.
		formatAndCheck(c, 1, "<div>Hello</div><div newline/><div>World</div>", true, opt)

		formatAndCheck(c, 1, "<div>Hello</div><br newline/><div>World</div>",
			"<div>Hello</div>\n\n<div>World</div>", opt)

		formatAndCheck(c, 1, "<div>Hello</div><br newline/><br newline/>World",
			"<div>Hello</div>\n\n\nWorld", opt)
	})

	c.Run("HTML element types", func(c *qt.C) {
		formatAndCheck(c, 2, "<pre>  <div>    Hello     </div>  </pre>", "<pre>  <div>    Hello     </div>  </pre>")
		formatAndCheck(c, 2, "<!DOCTYPE html><html><body><!-- comment1 --></body></html>", "<!DOCTYPE html>\n<html>\n  <body>\n    <!-- comment1 -->\n  </body>\n</html>")
		formatAndCheck(c, 2, "<div><p>AAA<br>BBB></p></div>", "<div>\n  <p>\n    AAA\n    <br>\n    BBB>\n  </p>\n</div>")
		formatAndCheck(c, 2, "<script>\nvar l1;\nvar l2;</script>", "<script>\n  var l1;\n  var l2;\n</script>")
		formatAndCheck(c, 2, "<div><p>AAA</p></div>", "<div>\n  <p>AAA</p>\n</div>")
		formatAndCheck(c, 2, "<code>  <div>    Hello     </div>  </code>", "<code>  <div>    Hello     </div>  </code>")
		formatAndCheck(c, 2, "<!-- comment1 --><!-- comment2 -->", "<!-- comment1 -->\n<!-- comment2 -->")
		formatAndCheck(c, 2, `<div class="foo" id="bar"></div>`, `<div class="foo" id="bar"></div>`)
	})

	c.Run("Custom text formatter", func(c *qt.C) {
		formatAndCheck(c, 2, `<script type="text/javascript">hello</script>`, "<script type=\"text/javascript\">HELLO</script>",
			WithTextFormatters(func(tag Tag) TextFormatter {
				if tag.Name != "script" {
					return nil
				}
				typ := tag.Attributes.ByKey("type")
				if typ.Value != "" && typ.Value != "text/javascript" {
					return nil
				}
				return func(text []byte, depth int) []byte {
					return bytes.ToUpper(text)
				}
			}))
	})

	c.Run("Text elements", func(c *qt.C) {
		formatAndCheck(c, 2, "<div>Hello <span>World</span>s</div>", "<div>Hello <span>World</span>s</div>")
		formatAndCheck(c, 2, "w3\n<br>", "w3\n<br>")
		formatAndCheck(c, 2, "<div></div>\nw3", "<div></div>\nw3")
		formatAndCheck(c, 2, fmt.Sprintf("<div>%s</div>", longTextWithoutNewlines), fmt.Sprintf("<div>\n  %s\n</div>", longTextWithoutNewlines))
		formatAndCheck(c, 2, "<br>\nw3", "<br>\nw3")
		formatAndCheck(c, 2, "Hello <span>World</span> and then.", "Hello <span>World</span> and then.")
		formatAndCheck(c, 2, "Hello\n<span>World</span>\nand then.", "Hello <span>World</span> and then.")
		formatAndCheck(c, 2, fmt.Sprintf("<div><span>%s</span></div>", longTextWithoutNewlines), "<div>\n  <span>\n    a bbaa bbaa bbaa bbaa bbaa bbaa bbaa bbaa bbaa bbaa bbaa bba\n  </span>\n</div>")
		formatAndCheck(c, 2, fmt.Sprintf("<div>%s</div>", longTextWithNewlines), "<div>\n  a bbaa bbaa bbaa bbaa bba\n  a bbaa bbaa bbaa bbaa bba\n  a bbaa bbaa bbaa bbaa bba\n</div>")
	})
}

func TestPrepareText(t *testing.T) {
	c := qt.New(t)

	text := prepareText([]byte("\tfoo"), []byte("%%"))
	c.Assert(text.hasNewline, qt.Equals, false)
	c.Assert(string(text.b), qt.Equals, "%%foo")

	text = prepareText([]byte("\tfoo\nfoo"), []byte("%%"))
	c.Assert(text.hasNewline, qt.Equals, true)
	c.Assert(string(text.b), qt.Equals, "\n%%foo\nfoo")
}

func TestFormatTextBlock(t *testing.T) {
	c := qt.New(t)

	f := func(in string, depth int) string {
		b := formatTextBlock([]byte("%"), []byte(in), depth)
		return string(b)
	}

	c.Assert(f("\nfoo\nbar", 1), qt.Equals, "\n%foo\n%bar")
	c.Assert(f("\nfoo\nbar\nbaz", 1), qt.Equals, "\n%foo\n%bar\n%baz")
	c.Assert(f("\nfoo\nbar\nbaz", 2), qt.Equals, "\n%%foo\n%%bar\n%%baz")
	c.Assert(f("\n foo\n  bar", 1), qt.Equals, "\n%foo\n% bar")
	c.Assert(f("\n          foo\n          bar", 1), qt.Equals, "\n%foo\n%bar")
}

var benchmarkHTML = `<!DOCTYPE html><html><head><title class="foo">This is a title.</title></head><body><p>Line1<br>` + longTextWithNewlines + `</p><br/></body></html> <!-- aaa -->`

func BenchmarkFormat(b *testing.B) {
	f := New()
	r := strings.NewReader(benchmarkHTML)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := f.Format(ioutil.Discard, r)
		if err != nil {
			b.Fatal(err)
		}
		r.Seek(0, 0)
	}
}

// Compare it with https://github.com/yosssi/gohtml
// Try to set it up as similar as possible creating
// a new reader on every iteration.
func BenchmarkFormatCompareHTMLFmt(b *testing.B) {
	for i := 0; i < b.N; i++ {
		f := New()
		r := strings.NewReader(benchmarkHTML)
		var buf bytes.Buffer
		err := f.Format(&buf, r)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Compare using https://github.com/yosssi/gohtml
func BenchmarkFormatCompareGoHTML(b *testing.B) {
	for i := 0; i < b.N; i++ {
		s := gohtml.Format(benchmarkHTML)
		if s == "" {
			b.Fatal("empty")
		}
	}
}
