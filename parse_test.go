package htmlfmt

import (
	"fmt"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestParse(t *testing.T) {
	c := qt.New(t)

	pc := func(c *qt.C, input string, matches ...string) {
		c.Helper()
		p := newParser(strings.NewReader(input), nil)
		tok, err := p.parse()

		c.Assert(err, qt.IsNil)
		c.Assert(tok, qt.Not(qt.IsNil))
		got := tok.String()

		for _, expect := range matches {
			c.Assert(got, qt.Contains, expect)
		}
	}

	c.Run("Basic", func(c *qt.C) {
		pc(c, `<div>Hi there</div>`, `START:typ(StartTag)-tag(div)-0[depth(0)|children(1)|size(13)]///typ(Text)-tag(div)-1[depth(1)|children(0)|size(8)]///typ(EndTag)-tag(div)-2[depth(0)|children(0)|size(6)]/0///:END`)
		pc(c, `<div>Hi <span>there</span></div>`, "START:typ(StartTag)-tag(div)-0[depth(0)|children(3)|size(26)]")
		pc(c, fmt.Sprintf("<div>%s</div>", strings.Repeat("<span>A</span>", 20)),
			"START:typ(StartTag)-tag(div)-0[depth(0)|children(40)|size(285)]", "typ(EndTag)-tag(div)-61[depth(0)|children(0)|size(6)]/0///:END")
	})

	c.Run("Preformatted", func(c *qt.C) {
		pc(c, `<div><pre><div>Text</div></pre></div>`, "StartTag-div-0[1:3:25]|StartTag-pre-1[2:0:5]|StartTag-div-2[2:1:9]|Text-div-3[2:0:4]|EndTag-div-4[2:0:6]|EndTag-pre-5[1:0:6]|EndTag-div-6[1:0:6]/0|")
	})

	c.Run("Formatted", func(c *qt.C) {
		pc(c, `<div>
  <div>Hello</div>
  <div>World</div>
</div>`, "StartTag-div-0[d0:c7:s37]|Text-div-1[d1:c0:s0]|StartTag-div-2[d1:c1:s10]|Text-div-3[d2:c0:s5]|EndTag-div-4[d1:c0:s6]/2|Text-div-5[d1:c0:s0]|StartTag-div-6[d1:c1:s10]|Text-div-7[d2:c0:s5]|EndTag-div-8[d1:c0:s6]/6|Text-div-9[d1:c0:s0]|EndTag-div-10[d0:c0:s6]/0|")
	})
}
