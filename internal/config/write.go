package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// configBackups is how many timestamped backups of the configuration file are kept.
const configBackups = 2

// SetPlugIP changes the ip of one plug in the YAML file at path. Only that value changes: comments,
// quoting and the rest of the file are kept. The previous file is copied to a timestamped .bak file first.
func SetPlugIP(path, id, ip string) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%q is not a valid IP address", ip)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	edited, err := replacePlugIP(data, id, ip)
	if err != nil {
		return err
	}
	if err := backupConfig(path, data, info.Mode().Perm()); err != nil {
		return err
	}
	return writeFileAtomic(path, edited, info.Mode().Perm())
}

func replacePlugIP(data []byte, id, ip string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("config file is empty")
	}
	_, plugs := mappingPair(doc.Content[0], "plugs")
	if plugs == nil || plugs.Kind != yaml.SequenceNode {
		return nil, errors.New(`config file has no "plugs" list`)
	}
	for _, plug := range plugs.Content {
		if _, idNode := mappingPair(plug, "id"); idNode == nil || idNode.Value != id {
			continue
		}
		key, val := mappingPair(plug, "ip")
		if val == nil {
			return nil, fmt.Errorf("plug %q has no ip in the config file", id)
		}
		edited, err := spliceScalar(data, key, val, ip)
		if err != nil {
			return nil, fmt.Errorf("plug %q: %w", id, err)
		}
		if got, err := plugIP(edited, id); err != nil || got != ip {
			return nil, fmt.Errorf("plug %q: the edit did not produce the expected ip", id)
		}
		return edited, nil
	}
	return nil, fmt.Errorf("no plug with id %q in the config file", id)
}

// mappingPair returns the key and value nodes of key in a YAML mapping, or nils when it is absent.
func mappingPair(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// spliceScalar replaces the text of a scalar that sits on the same line as its key.
// The value keeps its quotes and any trailing comment.
func spliceScalar(data []byte, key, val *yaml.Node, ip string) ([]byte, error) {
	if val.Kind != yaml.ScalarNode || val.Line == 0 || key.Line != val.Line {
		return nil, errors.New("ip must be on the same line as its key for etapo to edit it")
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if val.Line > len(lines) {
		return nil, errors.New("config file changed while it was being read")
	}
	line := lines[val.Line-1]
	start := byteOffset(line, val.Column)
	if start >= len(line) {
		return nil, errors.New("could not find the ip in the config file")
	}

	var token []byte // the value as written, including quotes
	var replacement string
	if q := line[start]; q == '"' || q == '\'' {
		end := bytes.IndexByte(line[start+1:], q)
		if end < 0 {
			return nil, errors.New("ip has no closing quote")
		}
		token = line[start : start+1+end+1]
		replacement = string(q) + ip + string(q)
	} else {
		rest := line[start:]
		if i := bytes.Index(rest, []byte(" #")); i >= 0 {
			rest = rest[:i]
		}
		token = bytes.TrimRight(rest, " \t\r\n")
		replacement = ip
	}
	if strings.Trim(string(token), `"'`) != val.Value {
		return nil, errors.New("could not match the ip in the config file")
	}

	edited := make([]byte, 0, len(line)+len(replacement))
	edited = append(edited, line[:start]...)
	edited = append(edited, replacement...)
	edited = append(edited, line[start+len(token):]...)
	lines[val.Line-1] = edited
	return bytes.Join(lines, nil), nil
}

// byteOffset converts a 1-based column, counted in characters, into a byte offset within line.
func byteOffset(line []byte, column int) int {
	offset := 0
	for c := 1; c < column && offset < len(line); c++ {
		_, size := utf8.DecodeRune(line[offset:])
		offset += size
	}
	return offset
}

// plugIP reads the ip of one plug from YAML data.
func plugIP(data []byte, id string) (string, error) {
	var file struct {
		Plugs []PlugConfig `yaml:"plugs"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return "", err
	}
	for _, p := range file.Plugs {
		if p.ID == id {
			return p.IP, nil
		}
	}
	return "", fmt.Errorf("no plug %q", id)
}

// backupConfig writes data to a timestamped copy next to the config file and keeps only the newest configBackups copies.
func backupConfig(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name := base + "-" + time.Now().Format("20060102-150405") + ".bak"
	if err := os.WriteFile(filepath.Join(dir, name), data, perm); err != nil {
		return fmt.Errorf("failed to back up config file: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(base) + `-\d{8}-\d{6}\.bak$`)
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && pattern.MatchString(e.Name()) {
			backups = append(backups, e.Name())
		}
	}
	sort.Strings(backups) // The timestamp in the name sorts oldest first
	for len(backups) > configBackups {
		_ = os.Remove(filepath.Join(dir, backups[0]))
		backups = backups[1:]
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write config file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}
