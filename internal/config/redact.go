package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"cistern/internal/admin"
)

const Redacted = "<redacted>"

type LiteralSecret struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

var secretFields = map[string]bool{
	"secret_access_key": true, "session_token": true, "session_key": true, "client_secret": true,
	"api_token": true, "token": true, "password": true, "passwd": true,
}

var notSecretFields = map[string]bool{"access_key_id": true, "client_id": true, "chat_id": true}

func IsSecretField(key string) bool {
	k := strings.ToLower(key)
	if secretFields[k] {
		return true
	}
	if notSecretFields[k] {
		return false
	}
	for _, suffix := range []string{"_file", "_path", "_dir", "_id", "_claim"} {
		if strings.HasSuffix(k, suffix) {
			return false
		}
	}
	return admin.IsSecretKey(k)
}

var refRe = regexp.MustCompile(`\$\{[^}]*\}`)

type redaction struct {
	key   string
	value string
}

type redactor struct {
	lines   map[int][]redaction
	secrets []LiteralSecret
}

func RedactYAML(src []byte) (string, []LiteralSecret, error) {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	root, err := parseYAML(text)
	if err != nil {
		return "", nil, err
	}
	r := &redactor{lines: map[int][]redaction{}, secrets: []LiteralSecret{}}
	r.walk(root, "", "")
	lines := strings.Split(text, "\n")
	for ln, reds := range r.lines {
		if ln < 1 || ln > len(lines) {
			continue
		}
		lines[ln-1] = redactLine(lines[ln-1], reds)
	}
	return strings.Join(lines, "\n"), r.secrets, nil
}

func (r *redactor) walk(n *node, path, key string) {
	if n == nil {
		return
	}
	switch n.kind {
	case mappingNode:
		for _, k := range n.keys {
			if k == "hooks" {
				continue
			}
			r.walk(n.m[k], join(path, k), k)
		}
	case sequenceNode:
		for i, it := range n.items {
			label := strconv.Itoa(i)
			if it != nil && it.kind == mappingNode {
				if nm := it.m["name"]; nm != nil && nm.kind == scalarNode && nm.value != "" && !strings.ContainsAny(nm.value, "[]") {
					label = nm.value
				}
			}
			r.walk(it, path+"["+label+"]", "")
		}
	case scalarNode:
		if key == "" || n.value == "" {
			return
		}
		red, literal := redactValue(path, key, n.value)
		if red == n.value {
			return
		}
		r.lines[n.line] = append(r.lines[n.line], redaction{key: key, value: red})
		if literal {
			r.secrets = append(r.secrets, LiteralSecret{Path: path, Line: n.line})
		}
	}
}

var webhookPath = regexp.MustCompile(`^notify\.webhooks\[[^\]]*\]\.url$`)

func redactValue(path, key, v string) (string, bool) {
	if IsSecretField(key) {
		return redactSecret(v)
	}
	if !strings.Contains(v, "://") {
		return v, false
	}
	out, lit := redactUserinfo(v)
	if webhookPath.MatchString(path) {
		var litPath bool
		out, litPath = redactURLPath(out)
		lit = lit || litPath
	}
	return out, lit
}

func redactSecret(v string) (string, bool) {
	rest := strings.TrimSpace(refRe.ReplaceAllString(v, ""))
	if rest != "" {
		return Redacted, true
	}
	literal := false
	out := refRe.ReplaceAllStringFunc(v, func(ref string) string {
		name, def, ok := strings.Cut(ref[2:len(ref)-1], ":-")
		if !ok || def == "" {
			return ref
		}
		literal = true
		return "${" + name + ":-" + Redacted + "}"
	})
	return out, literal
}

func redactURLPath(v string) (string, bool) {
	scheme, rest, ok := strings.Cut(v, "://")
	if !ok {
		return v, false
	}
	i := strings.IndexAny(rest, "/?#")
	if i < 0 {
		return v, false
	}
	host, tail := rest[:i], rest[i:]
	if tail == "/" || refRe.MatchString(tail) {
		return v, false
	}
	return scheme + "://" + host + "/" + Redacted, true
}

func redactUserinfo(v string) (string, bool) {
	u, err := url.Parse(v)
	if err != nil || u.User == nil {
		return v, false
	}
	if _, has := u.User.Password(); !has {
		return v, false
	}
	at := strings.Index(v, "@")
	scheme := strings.Index(v, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return Redacted, true
	}
	userinfo := v[scheme+3 : at]
	user, pass, _ := strings.Cut(userinfo, ":")
	if strings.TrimSpace(refRe.ReplaceAllString(pass, "")) == "" {
		return v, false
	}
	return v[:scheme+3] + user + ":" + Redacted + v[at:], true
}

func redactLine(line string, reds []redaction) string {
	pos := 0
	for _, rd := range reds {
		start, end, quoted, ok := findValue(line, rd.key, pos)
		if !ok {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			return line[:indent] + "# " + Redacted + " (line hidden: it holds a secret that could not be redacted precisely)"
		}
		val := rd.value
		if quoted != 0 || needsQuote(val) {
			val = strconv.Quote(val)
		}
		line = line[:start] + val + line[end:]
		pos = start + len(val)
	}
	return line
}

func needsQuote(v string) bool { return v != Redacted }

func findValue(line, key string, from int) (start, end int, quoted byte, ok bool) {
	for _, form := range []string{key, `"` + key + `"`, `'` + key + `'`} {
		i := from
		for {
			j := strings.Index(line[i:], form)
			if j < 0 {
				break
			}
			k := i + j
			before := byte(' ')
			if k > 0 {
				before = line[k-1]
			}
			after := k + len(form)
			i = k + 1
			if !strings.ContainsRune(" \t{,-", rune(before)) && k != 0 {
				continue
			}
			p := after
			for p < len(line) && line[p] == ' ' {
				p++
			}
			if p >= len(line) || line[p] != ':' {
				continue
			}
			p++
			for p < len(line) && line[p] == ' ' {
				p++
			}
			s, e, q := scanScalar(line, p, flowDepth(line[:k]) > 0)
			return s, e, q, e > s || q != 0
		}
	}
	return 0, 0, 0, false
}

func flowDepth(s string) int {
	d := 0
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' {
				i++
			}
		case c == '"' || c == '\'':
			q = c
		case c == '{' || c == '[':
			d++
		case c == '}' || c == ']':
			d--
		}
	}
	return d
}

func scanScalar(line string, p int, flow bool) (int, int, byte) {
	if p >= len(line) {
		return p, p, 0
	}
	if q := line[p]; q == '"' || q == '\'' {
		for i := p + 1; i < len(line); i++ {
			switch {
			case q == '"' && line[i] == '\\':
				i++
			case line[i] == q && q == '\'' && i+1 < len(line) && line[i+1] == '\'':
				i++
			case line[i] == q:
				return p, i + 1, q
			}
		}
		return p, len(line), q
	}
	end := len(line)
	for i := p; i < len(line); i++ {
		c := line[i]
		if flow && (c == ',' || c == '}' || c == ']') {
			end = i
			break
		}
		if c == '#' && i > p && (line[i-1] == ' ' || line[i-1] == '\t') {
			end = i
			break
		}
	}
	for end > p && (line[end-1] == ' ' || line[end-1] == '\t') {
		end--
	}
	return p, end, 0
}

func (s LiteralSecret) String() string { return fmt.Sprintf("%s (line %d)", s.Path, s.Line) }
