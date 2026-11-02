package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Parser tập con YAML đủ cho file cấu hình Cistern:
//   - mapping và sequence dạng khối theo thụt lề (chỉ dấu cách), kể cả "- key: v" gọn
//   - flow [a, b] và {k: v} (lồng nhau được)
//   - scalar thường, '...' và "..."; chú thích #
// Không hỗ trợ: chuỗi nhiều dòng (| >), anchor/alias (& *), tag (!), nhiều document.

type nodeKind int

const (
	scalarNode nodeKind = iota
	mappingNode
	sequenceNode
)

type node struct {
	kind   nodeKind
	value  string // scalar
	quoted bool
	keys   []string // mapping, theo thứ tự
	m      map[string]*node
	items  []*node // sequence
	line   int
}

func (n *node) isNull() bool {
	if n == nil {
		return true
	}
	if n.kind != scalarNode || n.quoted {
		return false
	}
	switch n.value {
	case "", "~", "null", "Null", "NULL":
		return true
	}
	return false
}

type line struct {
	num    int
	indent int
	text   string // đã bỏ thụt lề, chú thích và khoảng trắng cuối
}

type yamlError struct {
	line int
	msg  string
}

func (e *yamlError) Error() string { return fmt.Sprintf("line %d: %s", e.line, e.msg) }

func errAt(l int, format string, a ...any) error {
	return &yamlError{line: l, msg: fmt.Sprintf(format, a...)}
}

func parseYAML(src string) (*node, error) {
	var lines []line
	for i, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		n := i + 1
		trimmed := strings.TrimLeft(raw, " ")
		indent := len(raw) - len(trimmed)
		if strings.HasPrefix(trimmed, "\t") {
			return nil, errAt(n, "tab indentation is not allowed")
		}
		text := strings.TrimRight(stripComment(trimmed), " \t")
		if text == "" {
			continue
		}
		if text == "---" || text == "..." {
			if len(lines) == 0 && text == "---" {
				continue
			}
			return nil, errAt(n, "multiple documents are not supported")
		}
		lines = append(lines, line{num: n, indent: indent, text: text})
	}
	if len(lines) == 0 {
		return &node{kind: mappingNode, m: map[string]*node{}}, nil
	}
	p := &parser{lines: lines}
	if lines[0].indent != 0 {
		return nil, errAt(lines[0].num, "unexpected indentation")
	}
	n, err := p.parseBlock(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		l := p.lines[p.pos]
		return nil, errAt(l.num, "unexpected content %q (check indentation)", l.text)
	}
	return n, nil
}

// stripComment bỏ "# ..." nằm ngoài dấu nháy, ở đầu dòng hoặc sau khoảng trắng.
func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					q = 0
				}
			} else if c == '\\' && q == '"' {
				i++
			}
		case c == '\'' || c == '"':
			if i == 0 || strings.ContainsRune(" \t[{,:-", rune(s[i-1])) {
				q = c
			}
		case c == '#':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return s[:i]
			}
		}
	}
	return s
}

type parser struct {
	lines []line
	pos   int
}

func isSeqItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

func (p *parser) parseBlock(indent int) (*node, error) {
	l := p.lines[p.pos]
	if isSeqItem(l.text) {
		return p.parseSequence(indent)
	}
	if _, _, ok := splitKey(l.text); ok {
		return p.parseMapping(indent)
	}
	// Một scalar đứng riêng một dòng (giá trị khối của key phía trên).
	p.pos++
	return parseInline(l.text, l.num)
}

func (p *parser) parseSequence(indent int) (*node, error) {
	seq := &node{kind: sequenceNode, line: p.lines[p.pos].num}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, errAt(l.num, "bad indentation")
		}
		if !isSeqItem(l.text) {
			break
		}
		rest := strings.TrimLeft(strings.TrimPrefix(l.text, "-"), " ")
		var item *node
		var err error
		switch {
		case rest == "":
			p.pos++
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				item, err = p.parseBlock(p.lines[p.pos].indent)
			} else {
				item = &node{kind: scalarNode, line: l.num}
			}
		case isSeqItem(rest) || isBlockKey(rest):
			// "- key: v" hoặc "- - x": coi phần sau "- " như một dòng thụt lề sâu hơn.
			off := len(l.text) - len(rest)
			p.lines[p.pos] = line{num: l.num, indent: indent + off, text: rest}
			item, err = p.parseBlock(indent + off)
		default:
			p.pos++
			item, err = parseInline(rest, l.num)
		}
		if err != nil {
			return nil, err
		}
		seq.items = append(seq.items, item)
	}
	return seq, nil
}

func isBlockKey(text string) bool {
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{") {
		return false
	}
	_, _, ok := splitKey(text)
	return ok
}

func (p *parser) parseMapping(indent int) (*node, error) {
	m := &node{kind: mappingNode, m: map[string]*node{}, line: p.lines[p.pos].num}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, errAt(l.num, "bad indentation")
		}
		if isSeqItem(l.text) {
			return nil, errAt(l.num, "unexpected sequence item in mapping")
		}
		key, val, ok := splitKey(l.text)
		if !ok {
			return nil, errAt(l.num, "expected \"key: value\", got %q", l.text)
		}
		if _, dup := m.m[key]; dup {
			return nil, errAt(l.num, "duplicate key %q", key)
		}
		p.pos++
		var child *node
		var err error
		if val == "" {
			switch {
			case p.pos < len(p.lines) && p.lines[p.pos].indent > indent:
				child, err = p.parseBlock(p.lines[p.pos].indent)
			case p.pos < len(p.lines) && p.lines[p.pos].indent == indent && isSeqItem(p.lines[p.pos].text):
				child, err = p.parseSequence(indent)
			default:
				child = &node{kind: scalarNode, line: l.num}
			}
		} else {
			child, err = parseInline(val, l.num)
		}
		if err != nil {
			return nil, err
		}
		m.keys = append(m.keys, key)
		m.m[key] = child
	}
	return m, nil
}

// splitKey tách "key: value" / "key:" ngoài dấu nháy và ngoặc flow.
func splitKey(text string) (key, val string, ok bool) {
	var q byte
	depth := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' {
				i++
			}
		case (c == '"' || c == '\'') && i == 0:
			q = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ':' && depth == 0 && (i+1 == len(text) || text[i+1] == ' '):
			k := strings.TrimSpace(text[:i])
			if k == "" {
				return "", "", false
			}
			if k[0] == '"' || k[0] == '\'' {
				uk, err := unquote(k)
				if err != nil {
					return "", "", false
				}
				k = uk
			}
			return k, strings.TrimSpace(text[i+1:]), true
		}
	}
	return "", "", false
}

func unquote(s string) (string, error) {
	if len(s) < 2 || s[len(s)-1] != s[0] {
		return "", fmt.Errorf("unterminated string %s", s)
	}
	if s[0] == '\'' {
		inner := s[1 : len(s)-1]
		if strings.Count(inner, "'")%2 != 0 {
			return "", fmt.Errorf("bad single-quoted string %s", s)
		}
		return strings.ReplaceAll(inner, "''", "'"), nil
	}
	return strconv.Unquote(s)
}

func parseInline(s string, ln int) (*node, error) {
	switch s[0] {
	case '|', '>':
		return nil, errAt(ln, "multi-line strings (| >) are not supported")
	case '&', '*', '!':
		return nil, errAt(ln, "anchors, aliases and tags are not supported")
	case '[', '{':
		f := &flow{s: s, line: ln}
		n, err := f.value()
		if err != nil {
			return nil, err
		}
		f.space()
		if f.i != len(f.s) {
			return nil, errAt(ln, "unexpected %q after flow value", f.s[f.i:])
		}
		return n, nil
	case '"', '\'':
		v, err := unquote(s)
		if err != nil {
			return nil, errAt(ln, "%v", err)
		}
		return &node{kind: scalarNode, value: v, quoted: true, line: ln}, nil
	}
	return &node{kind: scalarNode, value: s, line: ln}, nil
}

type flow struct {
	s    string
	i    int
	line int
}

func (f *flow) space() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

func (f *flow) value() (*node, error) {
	f.space()
	if f.i >= len(f.s) {
		return nil, errAt(f.line, "unexpected end of flow value")
	}
	switch f.s[f.i] {
	case '[':
		f.i++
		seq := &node{kind: sequenceNode, line: f.line}
		for {
			f.space()
			if f.i < len(f.s) && f.s[f.i] == ']' {
				f.i++
				return seq, nil
			}
			item, err := f.value()
			if err != nil {
				return nil, err
			}
			seq.items = append(seq.items, item)
			if err := f.sep(']'); err != nil {
				return nil, err
			}
		}
	case '{':
		f.i++
		m := &node{kind: mappingNode, m: map[string]*node{}, line: f.line}
		for {
			f.space()
			if f.i < len(f.s) && f.s[f.i] == '}' {
				f.i++
				return m, nil
			}
			k, err := f.scalar(true)
			if err != nil {
				return nil, err
			}
			f.space()
			if f.i >= len(f.s) || f.s[f.i] != ':' {
				return nil, errAt(f.line, "expected ':' in flow mapping")
			}
			f.i++
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			if _, dup := m.m[k.value]; dup {
				return nil, errAt(f.line, "duplicate key %q", k.value)
			}
			m.keys = append(m.keys, k.value)
			m.m[k.value] = v
			if err := f.sep('}'); err != nil {
				return nil, err
			}
		}
	}
	return f.scalar(false)
}

func (f *flow) sep(closer byte) error {
	f.space()
	if f.i >= len(f.s) {
		return errAt(f.line, "unterminated flow collection")
	}
	switch f.s[f.i] {
	case ',':
		f.i++
		return nil
	case closer:
		return nil
	}
	return errAt(f.line, "expected ',' or %q", closer)
}

func (f *flow) scalar(isKey bool) (*node, error) {
	f.space()
	start := f.i
	if f.i < len(f.s) && (f.s[f.i] == '"' || f.s[f.i] == '\'') {
		q := f.s[f.i]
		f.i++
		for f.i < len(f.s) {
			c := f.s[f.i]
			if c == '\\' && q == '"' {
				f.i += 2
				continue
			}
			if c == q {
				if q == '\'' && f.i+1 < len(f.s) && f.s[f.i+1] == '\'' {
					f.i += 2
					continue
				}
				f.i++
				v, err := unquote(f.s[start:f.i])
				if err != nil {
					return nil, errAt(f.line, "%v", err)
				}
				return &node{kind: scalarNode, value: v, quoted: true, line: f.line}, nil
			}
			f.i++
		}
		return nil, errAt(f.line, "unterminated string")
	}
	for f.i < len(f.s) {
		c := f.s[f.i]
		if c == ',' || c == ']' || c == '}' || c == '[' || c == '{' {
			break
		}
		if isKey && c == ':' {
			break
		}
		if !isKey && c == ':' && f.i+1 < len(f.s) && f.s[f.i+1] == ' ' {
			break
		}
		f.i++
	}
	return &node{kind: scalarNode, value: strings.TrimSpace(f.s[start:f.i]), line: f.line}, nil
}
