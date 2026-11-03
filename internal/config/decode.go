package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func expandEnv(n *node, lookup func(string) (string, bool), path string, lenient func(string) bool) error {
	if n == nil {
		return nil
	}
	switch n.kind {
	case scalarNode:
		v, err := expandString(n.value, lookup, lenient(path))
		if err != nil {
			return errAt(n.line, "%v", err)
		}
		n.value = v
	case sequenceNode:
		for _, it := range n.items {
			if err := expandEnv(it, lookup, path, lenient); err != nil {
				return err
			}
		}
	case mappingNode:
		for _, k := range n.keys {
			if k == "hooks" {
				continue
			}
			if err := expandEnv(n.m[k], lookup, join(path, k), lenient); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandString(s string, lookup func(string) (string, bool), lenient bool) (string, error) {
	var sb strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			sb.WriteString(s)
			return sb.String(), nil
		}
		sb.WriteString(s[:i])
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated ${ in %q", s)
		}
		expr := s[i+2 : i+end]
		name, def, hasDef := strings.Cut(expr, ":-")
		if !validEnvName(name) {
			return "", fmt.Errorf("invalid variable reference ${%s}", expr)
		}
		v, ok := lookup(name)
		switch {
		case hasDef && v == "":
			v = def
		case !ok && !lenient:
			return "", fmt.Errorf("environment variable %s is not set and has no default", name)
		}
		sb.WriteString(v)
		s = s[i+end+1:]
	}
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

var durationType = reflect.TypeOf(time.Duration(0))

func decode(n *node, v reflect.Value, path string) error {
	if n.isNull() && v.Kind() != reflect.String {
		return nil
	}
	fail := func(format string, a ...any) error {
		return fmt.Errorf("%s (line %d): %s", path, n.line, fmt.Sprintf(format, a...))
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return decode(n, v.Elem(), path)
	}
	if v.Type() == durationType {
		if n.kind != scalarNode {
			return fail("expected a duration")
		}
		d, err := parseDuration(n.value)
		if err != nil {
			return fail("%v", err)
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.Struct:
		if n.kind != mappingNode {
			return fail("expected a mapping")
		}
		fields := map[string]int{}
		for i := 0; i < v.NumField(); i++ {
			if tag := v.Type().Field(i).Tag.Get("yaml"); tag != "" && tag != "-" {
				fields[tag] = i
			}
		}
		for _, k := range n.keys {
			idx, ok := fields[k]
			if !ok {
				return fmt.Errorf("%s (line %d): unknown key", join(path, k), n.m[k].line)
			}
			if err := decode(n.m[k], v.Field(idx), join(path, k)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if n.kind != mappingNode {
			return fail("expected a mapping")
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		for _, k := range n.keys {
			ev := reflect.New(v.Type().Elem()).Elem()
			if err := decode(n.m[k], ev, join(path, k)); err != nil {
				return err
			}
			v.SetMapIndex(reflect.ValueOf(k), ev)
		}
	case reflect.Slice:
		if n.kind != sequenceNode {
			return fail("expected a list")
		}
		s := reflect.MakeSlice(v.Type(), len(n.items), len(n.items))
		for i, it := range n.items {
			if err := decode(it, s.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		v.Set(s)
	case reflect.String:
		switch {
		case n.kind == scalarNode:
			if n.isNull() {
				v.SetString("")
			} else {
				v.SetString(n.value)
			}
		case n.kind == sequenceNode:

			parts := make([]string, len(n.items))
			for i, it := range n.items {
				if it.kind != scalarNode {
					return fail("expected a list of scalars")
				}
				parts[i] = it.value
			}
			v.SetString(strings.Join(parts, ","))
		default:
			return fail("expected a scalar")
		}
	case reflect.Bool:
		if n.kind != scalarNode {
			return fail("expected true or false")
		}
		switch strings.ToLower(n.value) {
		case "true":
			v.SetBool(true)
		case "false":
			v.SetBool(false)
		default:
			return fail("expected true or false, got %q", n.value)
		}
	case reflect.Int, reflect.Int64:
		if n.kind != scalarNode {
			return fail("expected an integer")
		}
		i, err := strconv.ParseInt(n.value, 10, 64)
		if err != nil {
			return fail("expected an integer, got %q", n.value)
		}
		v.SetInt(i)
	default:
		return fail("unsupported field type %s", v.Type())
	}
	return nil
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

func osLookup(name string) (string, bool) { return os.LookupEnv(name) }
