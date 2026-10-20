package config

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A reader for ssh_config, used to DISCOVER hosts — never to decide how to reach
// them.
//
// ftop connects with plain `ssh <alias>`, so OpenSSH itself applies the whole
// configuration: Include, Match, ProxyJump, ProxyCommand, certificates and
// everything else. This reader only has to answer "which machines does this file
// name?" and, for display, "where does each one point?". It therefore accepts any
// file OpenSSH accepts: it follows Include, skips Match blocks (they name
// conditions, not machines), and ignores keywords it does not know. It never runs
// anything, which is why discovery does not ask ssh (`ssh -G` executes Match exec
// hooks).

// sshConfig is a parsed ssh_config.
type sshConfig struct {
	path   string
	blocks []block // in file order; the first may be the global section
}

type block struct {
	patterns []string // nil for the section before the first Host line
	// match marks a Match block. Its options are not evaluated (the condition
	// depends on runtime state) and it names no machine.
	match bool
	opts  []opt
}

type opt struct {
	key  string // lowercased
	args []string
	line int
}

const maxSSHConfigSize = 1 << 20

// maxIncludeDepth bounds Include recursion, as OpenSSH does (READCONF_MAX_DEPTH).
const maxIncludeDepth = 16

// multiKeys accumulate across stanzas instead of first-value-wins.
var multiKeys = set("identityfile", "certificatefile", "localforward", "remoteforward",
	"dynamicforward", "sendenv", "setenv")

// synonyms are keywords OpenSSH treats as one option under two names.
var synonyms = map[string]string{
	"pubkeyacceptedkeytypes":          "pubkeyacceptedalgorithms",
	"hostbasedkeytypes":               "hostbasedacceptedalgorithms",
	"challengeresponseauthentication": "kbdinteractiveauthentication",
	"keepalive":                       "tcpkeepalive",
}

func set(ks ...string) map[string]bool {
	m := make(map[string]bool, len(ks))
	for _, k := range ks {
		m[k] = true
	}
	return m
}

// readSSHConfig parses path and everything it includes. A missing file is an
// empty configuration.
func readSSHConfig(path string) (*sshConfig, error) {
	c := &sshConfig{path: path, blocks: []block{{}}}
	if path == "" {
		return c, nil
	}
	if err := c.readFile(path, 0); err != nil {
		return nil, err
	}
	return c, nil
}

// readFile appends path's lines to c. An Include is read in place, so a Host line
// inside an included file starts a new block exactly where the Include stood.
func (c *sshConfig) readFile(path string, depth int) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("%s: Include nested deeper than %d", path, maxIncludeDepth)
	}
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// A FIFO or device would block the read forever (or feed it anything).
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > maxSSHConfigSize {
		return fmt.Errorf("%s is larger than %d bytes", path, maxSSHConfigSize)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxSSHConfigSize)
	n := 0
	for sc.Scan() {
		n++
		toks, err := tokenize(sc.Text())
		if err != nil {
			return fmt.Errorf("%s:%d: %v", path, n, err)
		}
		if len(toks) == 0 {
			continue
		}
		key, args := strings.ToLower(toks[0]), toks[1:]
		if canon, ok := synonyms[key]; ok {
			key = canon
		}
		switch key {
		case "host":
			if len(args) == 0 {
				return fmt.Errorf("%s:%d: Host without a pattern", path, n)
			}
			c.blocks = append(c.blocks, block{patterns: args})
			continue
		case "match":
			c.blocks = append(c.blocks, block{match: true})
			continue
		case "include":
			for _, pat := range args {
				files, err := includeFiles(pat)
				if err != nil {
					return fmt.Errorf("%s:%d: %v", path, n, err)
				}
				for _, inc := range files {
					if err := c.readFile(inc, depth+1); err != nil {
						return err
					}
				}
			}
			continue
		}
		cur := &c.blocks[len(c.blocks)-1]
		cur.opts = append(cur.opts, opt{key: key, args: args, line: n})
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	return nil
}

// includeFiles expands one Include argument the way OpenSSH does for a user
// configuration: a leading ~ is the home directory, a relative path is relative to
// ~/.ssh, and the result is a glob whose matches are read in lexical order.
func includeFiles(pat string) ([]string, error) {
	home, _ := os.UserHomeDir()
	switch {
	case pat == "~":
		pat = home
	case strings.HasPrefix(pat, "~/"):
		pat = filepath.Join(home, pat[2:])
	case !filepath.IsAbs(pat):
		pat = filepath.Join(home, ".ssh", pat)
	}
	files, err := filepath.Glob(pat)
	if err != nil {
		return nil, fmt.Errorf("Include %s: %v", pat, err)
	}
	sort.Strings(files)
	return files, nil
}

// tokenize splits a config line exactly as OpenSSH's readconf does: the keyword
// by strdelim (whitespace or one '=' with any whitespace around it, a double-
// quoted keyword allowed), the rest by argv_split (single and double quotes,
// backslash escapes for quote, backslash and space, '#' at the start of a token
// ends the line). Control characters are rejected rather than interpreted.
func tokenize(line string) ([]string, error) {
	for _, r := range line {
		if (r < 0x20 && r != '\t' && r != '\r' && r != '\n' && r != '\f') || r == 0x7f {
			return nil, fmt.Errorf("control character")
		}
	}
	s := strings.TrimRight(line, " \t\r\n\f")
	s = strings.TrimLeft(s, " \t")
	if s == "" || s[0] == '#' {
		return nil, nil
	}
	var key, rest string
	i := strings.IndexAny(s, " \t\"=")
	switch {
	case i < 0:
		key, rest = s, ""
	case s[i] == '"':
		j := strings.IndexByte(s[i+1:], '"')
		if j < 0 {
			return nil, fmt.Errorf("unterminated quote")
		}
		key = s[:i] + s[i+1:i+1+j]
		rest = strings.TrimLeft(s[i+2+j:], " \t")
	default:
		equals := s[i] == '='
		key = s[:i]
		rest = strings.TrimLeft(s[i+1:], " \t")
		if !equals && strings.HasPrefix(rest, "=") {
			rest = strings.TrimLeft(rest[1:], " \t")
		}
	}
	if key == "" || key[0] == '#' {
		return nil, nil
	}
	args, err := argvSplit(rest)
	if err != nil {
		return nil, err
	}
	return append([]string{key}, args...), nil
}

// argvSplit is OpenSSH's argv_split(s, …, terminate_on_comment=1).
func argvSplit(s string) ([]string, error) {
	var out []string
	i := 0
	for i < len(s) {
		if s[i] == ' ' || s[i] == '\t' {
			i++
			continue
		}
		if s[i] == '#' {
			break
		}
		var arg strings.Builder
		var quote byte
		for ; i < len(s); i++ {
			c := s[i]
			switch {
			case c == '\\' && i+1 < len(s) && (s[i+1] == '\'' || s[i+1] == '"' || s[i+1] == '\\' || (quote == 0 && s[i+1] == ' ')):
				i++
				arg.WriteByte(s[i])
			case quote == 0 && (c == ' ' || c == '\t'):
				goto done
			case quote == 0 && (c == '"' || c == '\''):
				quote = c
			case quote != 0 && c == quote:
				quote = 0
			default:
				arg.WriteByte(c)
			}
		}
		if quote != 0 {
			return nil, fmt.Errorf("unterminated quote")
		}
	done:
		out = append(out, arg.String())
	}
	return out, nil
}

// hostMatches implements ssh's Host matching: patterns are compared with the
// destination as given, case-sensitively (OpenSSH: `ssh -G FOO` does not match
// `Host Foo`), * and ? only, a negated pattern vetoes the line, and at least one
// positive pattern must match.
func hostMatches(patterns []string, host string) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		if neg {
			p = p[1:]
		}
		if sshWildcard(p, host) {
			if neg {
				return false
			}
			matched = true
		}
	}
	return matched
}

// sshWildcard is ssh's match_pattern (* and ?, byte-wise), done iteratively:
// the recursive form backtracks exponentially, and a config line of 36 stars
// hung startup.
func sshWildcard(p, s string) bool {
	px, sx, star, mark := 0, 0, -1, 0
	for sx < len(s) {
		switch {
		case px < len(p) && (p[px] == '?' || p[px] == s[sx]):
			px++
			sx++
		case px < len(p) && p[px] == '*':
			star, mark = px, sx
			px++
		case star >= 0:
			px = star + 1
			mark++
			sx = mark
		default:
			return false
		}
	}
	for px < len(p) && p[px] == '*' {
		px++
	}
	return px == len(p)
}

// view is the evaluated configuration for one destination.
type view struct {
	host, port, user string
	// opts maps each keyword to its values, each value being the keyword's
	// arguments as written (so a quoted path with spaces stays one argument).
	opts map[string][][]string
	// configured is true when a non-wildcard Host pattern matched or HostName
	// was set: the name means something in this configuration.
	configured bool
}

func (v view) first(k string) string {
	if a := v.opts[k]; len(a) > 0 {
		return strings.Join(a[0], " ")
	}
	return ""
}

// eval evaluates dest ("alias", "user@alias", "ssh://user@alias:port") the way
// ssh would: every matching stanza in file order, first value wins, multi-value
// keywords accumulate, and the destination's own user and port take precedence.
func (c *sshConfig) eval(dest string) (view, error) {
	duser, dhost, dport := splitDest(dest)
	v := view{opts: map[string][][]string{}}
	for _, b := range c.blocks {
		// A Match block's condition depends on runtime state; its options are left
		// to ssh, which evaluates them when it connects.
		if b.match {
			continue
		}
		if b.patterns != nil {
			if !hostMatches(b.patterns, dhost) {
				continue
			}
			for _, p := range b.patterns {
				if !strings.ContainsAny(p, "*?!") {
					v.configured = true
				}
			}
		}
		for _, o := range b.opts {
			if multiKeys[o.key] {
				v.opts[o.key] = append(v.opts[o.key], o.args)
				continue
			}
			if _, set := v.opts[o.key]; !set {
				v.opts[o.key] = [][]string{o.args}
			}
		}
	}
	host := v.first("hostname")
	if host == "" {
		host = strings.ToLower(dhost) // OpenSSH lowercases the default
	} else {
		v.configured = true
		// Only %h and %% are expanded. The value is shown, never connected to, so
		// any other token is left as written rather than guessed at.
		host = expandHostToken(host, dhost)
	}
	v.host = host
	v.port = dport
	if v.port == "" {
		v.port = v.first("port")
	}
	if v.port == "" {
		v.port = "22"
	}
	pn, err := strconv.Atoi(v.port)
	if err != nil || pn <= 0 || pn > 65535 {
		return view{}, fmt.Errorf("port %q is not a valid port", v.port)
	}
	v.port = strconv.Itoa(pn)
	v.user = duser
	if v.user == "" {
		v.user = v.first("user")
	}
	if v.user == "" {
		if u, err := user.Current(); err == nil {
			v.user = u.Username
		}
	}
	return v, nil
}

func expandHostToken(h, alias string) string {
	var b strings.Builder
	for i := 0; i < len(h); i++ {
		if h[i] != '%' || i+1 >= len(h) {
			b.WriteByte(h[i])
			continue
		}
		i++
		switch h[i] {
		case 'h':
			b.WriteString(strings.ToLower(alias)) // as OpenSSH: %h is the lowercased host
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(h[i])
		}
	}
	return b.String()
}

// splitDest parses an ssh destination into user, host and port.
func splitDest(dest string) (string, string, string) {
	d := dest
	uri := strings.HasPrefix(d, "ssh://")
	d = strings.TrimPrefix(d, "ssh://")
	if i := strings.IndexByte(d, '/'); uri && i >= 0 {
		d = d[:i]
	}
	var usr, port string
	if i := strings.LastIndex(d, "@"); i >= 0 {
		usr, d = d[:i], d[i+1:]
		if uri {
			// OpenSSH percent-decodes the user of an ssh:// URI.
			if dec, err := url.PathUnescape(usr); err == nil {
				usr = dec
			}
		}
	}
	if uri {
		if strings.HasPrefix(d, "[") {
			if i := strings.IndexByte(d, ']'); i > 0 {
				rest := d[i+1:]
				d = d[1:i]
				port = strings.TrimPrefix(rest, ":")
			}
		} else if strings.Count(d, ":") == 1 {
			i := strings.IndexByte(d, ':')
			d, port = d[:i], d[i+1:]
		}
	}
	return usr, d, port
}

// discover lists the configuration's hosts: each Host line's literal names, the
// first as the name and the rest as its aliases. Every one of them is a machine
// ftop manages; a pattern (`Host *`, `Host gpu-*`) names options, not a machine.
func (c *sshConfig) discover() []Host {
	var out []Host
	for _, b := range c.blocks {
		var names []string
		for _, p := range b.patterns {
			if !strings.ContainsAny(p, "*?!") {
				names = append(names, p)
			}
		}
		if len(names) == 0 {
			continue
		}
		h := Host{Name: names[0], Aliases: names[1:]}
		if v, err := c.eval(names[0]); err == nil {
			h.Hostname = v.host
			h.Port = v.port
		}
		out = append(out, h)
	}
	return out
}
