package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Editable lists the keys Set and Unset accept, in the Settings view's
// order (REQ-CLI-016). data-dir is left out because it points at another
// index; claude.profiles because a profile is several lines.
var Editable = []string{"theme", "refresh", "recent-window", "providers",
	"codex.home", "codex.stale-after", "claude.home", "claude.stale-after",
	"serve.listen", "serve.public-url"}

// tempPattern names the temporary file a save writes beside the settings
// file, so the final rename stays on one file system.
const tempPattern = ".agentflockr.toml.*"

// Set writes key = value into the settings file at path, changing that
// key's line, or adding one, and nothing else. value is a string, or a
// []string for providers. It creates the file when there is none.
//
// The file is edited as text, because go-toml drops comments when it
// writes. The edit is safe because the result is read back: unless it holds
// the old settings with only this key changed, nothing is written.
func Set(path, key string, value any) error {
	encoded, err := encodeValue(value)
	if err != nil {
		return err
	}
	return edit(path, key, func(want *Settings) error {
		// The value is decoded as the file would decode it, so its type and
		// range are checked with the file's own messages.
		doc := key + " = " + encoded
		if err := toml.NewDecoder(strings.NewReader(doc)).DisallowUnknownFields().Decode(want); err != nil {
			return errors.New(strings.TrimPrefix(err.Error(), "toml: "))
		}
		return want.Validate()
	}, func(text string, r region) (string, bool) {
		if at, ok := r.find(text); ok {
			return text[:at.value[0]] + encoded + text[at.value[1]:], true
		}
		return r.add(text, r.name+" = "+encoded), true
	})
}

// Unset removes key's line from the settings file at path, so its default
// applies. A file that does not set the key is left as it was.
func Unset(path, key string) error {
	return edit(path, key, func(want *Settings) error {
		def := Default()
		field(want, key).Set(field(&def, key))
		return nil
	}, func(text string, r region) (string, bool) {
		at, ok := r.find(text)
		if !ok {
			// No line to remove. If the key is set some other way, the
			// read-back check refuses; if not, there is nothing to do.
			return text, false
		}
		return text[:at.line[0]] + text[at.line[1]:], true
	})
}

// edit runs the steps both Set and Unset take: refuse a key that is not
// editable, read and check the file, make the expected settings and the new
// text, verify one against the other, and replace the file.
func edit(path, key string, expect func(*Settings) error, change func(string, region) (string, bool)) error {
	if !editable(key) {
		return fmt.Errorf("%s can't be changed from Settings", key)
	}
	if path == "" {
		return errors.New("no settings file location is known")
	}
	target, err := filepath.EvalSymlinks(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		target = path
	case err != nil:
		return err
	}
	data, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	existed := err == nil
	old, err := decode(target, data)
	if err != nil {
		return err
	}
	want := old
	if err := expect(&want); err != nil {
		return err
	}
	text, changed := change(string(data), regionOf(key))
	if !changed {
		if reflect.DeepEqual(old, want) {
			return nil
		}
		return fmt.Errorf("could not change %s's line safely; change it in the file yourself", key)
	}
	got, err := decode(target, []byte(text))
	if err != nil || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("could not change %s's line safely; change it in the file yourself", key)
	}
	mode := fs.FileMode(0600)
	if existed {
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	return replace(target, []byte(text), mode)
}

// Names reports whether a settings file's text sets the dotted key. A text
// that is not valid TOML sets nothing.
func Names(data []byte, key string) bool {
	var v any
	if toml.Unmarshal(data, &v) != nil {
		return false
	}
	for _, part := range strings.Split(key, ".") {
		table, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if v, ok = table[part]; !ok {
			return false
		}
	}
	return true
}

func editable(key string) bool {
	for _, k := range Editable {
		if k == key {
			return true
		}
	}
	return false
}

// decode reads a settings file's text over the defaults, as Load does.
func decode(path string, data []byte) (Settings, error) {
	s := Default()
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&s); err != nil {
		return s, describe(path, err)
	}
	if err := s.Validate(); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// field finds the Settings field a dotted key names, by its TOML tags.
func field(s *Settings, key string) reflect.Value {
	v := reflect.ValueOf(s).Elem()
	for _, part := range strings.Split(key, ".") {
		t := v.Type()
		for i := range t.NumField() {
			if name, _, _ := strings.Cut(t.Field(i).Tag.Get("toml"), ","); name == part {
				v = v.Field(i)
				break
			}
		}
	}
	return v
}

// encodeValue writes a value as TOML text: a basic string in double
// quotes, or an array of them, as people write the file by hand.
func encodeValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return quote(v), nil
	case []string:
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = quote(s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	return "", fmt.Errorf("a setting can't hold a %T", value)
}

// quote writes s as a TOML basic string, escaping what the format requires.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// region is where a key's line belongs: the top level, before the first
// table header, or the lines of one table, from its header to the next.
type region struct {
	table, name string
}

func regionOf(key string) region {
	table, name, ok := strings.Cut(key, ".")
	if !ok {
		return region{name: key}
	}
	return region{table: table, name: name}
}

// lines splits text into lines, each keeping its line ending, with the
// offset where it starts.
func lines(text string) (out []string, starts []int) {
	at := 0
	for _, l := range strings.SplitAfter(text, "\n") {
		if l == "" {
			continue
		}
		out, starts = append(out, l), append(starts, at)
		at += len(l)
	}
	return out, starts
}

// isHeader reports whether a line starts a table or an array of tables.
func isHeader(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "[")
}

// headerRE matches a plain table header, such as "[codex]", with an
// optional comment.
func headerRE(table string) *regexp.Regexp {
	return regexp.MustCompile(`^[ \t]*\[[ \t]*` + regexp.QuoteMeta(table) + `[ \t]*\][ \t]*(#.*)?\r?\n?$`)
}

// span returns the region's line indexes [from, to), and the index of the
// table's header line, or -1 when the table has none.
func (r region) span(ls []string) (from, to, header int) {
	if r.table == "" {
		to = len(ls)
		for i, l := range ls {
			if isHeader(l) {
				to = i
				break
			}
		}
		return 0, to, -1
	}
	re := headerRE(r.table)
	for i, l := range ls {
		if !re.MatchString(l) {
			continue
		}
		to = len(ls)
		for j := i + 1; j < len(ls); j++ {
			if isHeader(ls[j]) {
				to = j
				break
			}
		}
		return i + 1, to, i
	}
	return 0, 0, -1
}

// match is where a key's line is: line is the whole line with its ending,
// value the value's text, both as offsets into the file.
type match struct{ line, value [2]int }

// find looks for the key's line in its region. The value must be a
// one-line string or array; any other form is not found, and the read-back
// check then refuses the duplicate key a new line would make.
func (r region) find(text string) (match, bool) {
	ls, starts := lines(text)
	from, to, _ := r.span(ls)
	re := regexp.MustCompile(`^[ \t]*` + regexp.QuoteMeta(r.name) + `[ \t]*=[ \t]*("[^"\\\n]*"|'[^'\n]*'|\[[^\]\n]*\])`)
	for i := from; i < to; i++ {
		if at := re.FindStringSubmatchIndex(ls[i]); at != nil {
			s := starts[i]
			return match{line: [2]int{s, s + len(ls[i])}, value: [2]int{s + at[2], s + at[3]}}, true
		}
	}
	return match{}, false
}

// add puts a new line in the region: first in the file for the top level,
// right after the table's header, or in a new table at the end. New lines
// end as the file's first line does.
func (r region) add(text, line string) string {
	eol := "\n"
	if first, _, ok := strings.Cut(text, "\n"); ok && strings.HasSuffix(first, "\r") {
		eol = "\r\n"
	}
	if r.table == "" {
		return line + eol + text
	}
	ls, starts := lines(text)
	if _, _, header := r.span(ls); header >= 0 {
		end := starts[header] + len(ls[header])
		head := text[:end]
		if !strings.HasSuffix(head, "\n") {
			head += eol
		}
		return head + line + eol + text[end:]
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += eol
	}
	if text != "" {
		text += eol
	}
	return text + "[" + r.table + "]" + eol + line + eol
}

// replace writes data to a temporary file beside target, with mode, and
// renames it over target, so a reader sees the old file or the new one,
// never a part. A missing folder is created private to the person.
func replace(target string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}
