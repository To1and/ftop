package config

import (
	"regexp"
	"strings"
	"sync"
)

// Shell-style matching for `drop` patterns and for reading ssh_config.

// destHost extracts the machine from an ssh destination: "ssh://u@h:22/x",
// "u@h" and "h" all give "h".
func destHost(dest string) string {
	d := strings.TrimPrefix(dest, "ssh://")
	if i := strings.LastIndex(d, "@"); i >= 0 {
		d = d[i+1:]
	}
	if i := strings.IndexByte(d, '/'); i >= 0 {
		d = d[:i]
	}
	if strings.HasPrefix(d, "[") { // [v6]:port
		if i := strings.IndexByte(d, ']'); i > 0 {
			return d[1:i]
		}
	}
	if strings.Count(d, ":") == 1 {
		d = d[:strings.IndexByte(d, ':')]
	}
	return d
}

// globMatch is a case-insensitive shell glob: * (spanning every character, '/'
// included), ?, [classes] with ! or ^ negation, ranges and escapes, and \
// escapes outside classes.
//
// filepath.Match stops * at '/', so `drop = ["*"]` did not match an
// ssh://user@host destination. Its classes must keep working: `drop =
// ["node-[12]"]` is existing syntax, and silently dropping support for it would
// reconnect hosts the user excluded.
func globMatch(pattern, s string) bool {
	v, ok := globCache.Load(pattern)
	if !ok {
		v, _ = globCache.LoadOrStore(pattern, compileGlob(pattern))
	}
	re, _ := v.(*regexp.Regexp)
	return re != nil && re.MatchString(s)
}

var globCache sync.Map

func compileGlob(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?is)^")
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '\\':
			if i+1 < len(rs) {
				i++
				b.WriteString(regexp.QuoteMeta(string(rs[i])))
			} else {
				b.WriteString(`\\`)
			}
		case '[':
			class, end, ok := globClass(rs, i)
			if !ok {
				b.WriteString(`\[`) // unterminated: a literal bracket
				continue
			}
			b.WriteString(class)
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// globClass translates the class starting at rs[start] == '[' into a regexp
// class and returns the index of its closing ']'. Inside a class \x is a literal
// x (so \] and \- are a bracket and a hyphen), a ']' first is literal, and an
// unescaped '-' between two members is a range.
func globClass(rs []rune, start int) (string, int, bool) {
	var b strings.Builder
	b.WriteByte('[')
	i := start + 1
	if i < len(rs) && (rs[i] == '!' || rs[i] == '^') {
		b.WriteByte('^')
		i++
	}
	first := true
	for ; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == ']' && !first:
			b.WriteByte(']')
			return b.String(), i, true
		case r == '\\' && i+1 < len(rs):
			i++
			b.WriteString(classLiteral(rs[i]))
		case r == '-' && !first && i+1 < len(rs) && rs[i+1] != ']':
			b.WriteByte('-') // a range operator
		default:
			b.WriteString(classLiteral(r))
		}
		first = false
	}
	return "", 0, false
}

// classLiteral escapes a rune for use as a literal inside a regexp class.
func classLiteral(r rune) string {
	switch r {
	case '\\', ']', '[', '^', '-':
		return `\` + string(r)
	}
	return string(r)
}

func globMatchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if globMatch(p, s) {
			return true
		}
	}
	return false
}
