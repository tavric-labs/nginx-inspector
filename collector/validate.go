package collector

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxEventBytes = 16384

type Event map[string]any

//go:embed native-schema.json
var schemaData []byte

type arraySpec struct {
	Cap    int               `json:"cap"`
	Fields map[string]string `json:"fields"`
}
type nativeSpec struct {
	Scalars map[string]map[string]string `json:"scalars"`
	Arrays  map[string]arraySpec         `json:"arrays"`
	Trace   []string                     `json:"trace"`
	Retry   []string                     `json:"retry"`
}

var spec = func() nativeSpec {
	var s nativeSpec
	if err := json.Unmarshal(schemaData, &s); err != nil {
		panic(err)
	}
	return s
}()
var idPattern = regexp.MustCompile(`^[0-9-]{1,100}$`)
var wirePattern = regexp.MustCompile(`^[0-9]{1,20}$`)
var integers = strings.Fields("started_ms duration_ms status request_bytes response_body_bytes connection_requests rewrite_visits worker_sent worker_dropped")
var nested = map[string]string{"phases_ms": "post_read rewrite access content header_filter first_body_filter", "request_headers": "content-type content-length", "response_headers": "content-type", "upstream": "address connect_s header_s response_s status cache_status", "tls": "version cipher"}

func contains(values []string, s string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func integer(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, e := n.Int64()
	return i, e == nil
}
func object(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("object required")
	}
	return m, nil
}
func checked(m map[string]any, fields map[string]string) error {
	for k, v := range m {
		kind, ok := fields[k]
		if !ok {
			return fmt.Errorf("unknown field %s", k)
		}
		if v == nil {
			continue
		}
		switch kind {
		case "str":
			s, ok := v.(string)
			if !ok || utf8.RuneCountInString(s) > 256 {
				return fmt.Errorf("invalid string")
			}
			if k == "path" || k == "initial_path" || k == "original_path" {
				m[k] = strings.SplitN(s, "?", 2)[0]
			}
		case "int":
			if _, ok := integer(v); !ok {
				return fmt.Errorf("invalid integer")
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("invalid boolean")
			}
		case "retry":
			a, ok := v.([]any)
			if !ok || len(a) > len(spec.Retry) {
				return fmt.Errorf("invalid retry list")
			}
			for _, x := range a {
				s, ok := x.(string)
				if !ok || !contains(spec.Retry, s) {
					return fmt.Errorf("invalid retry trigger")
				}
			}
		}
	}
	return nil
}
func Validate(raw []byte) (Event, error) {
	if len(raw) > MaxEventBytes || !utf8.Valid(raw) {
		return nil, fmt.Errorf("oversized or invalid encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var e map[string]any
	if err := d.Decode(&e); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	schema, ok := integer(e["schema"])
	if !ok || (schema != 1 && schema != 2) {
		return nil, fmt.Errorf("unknown schema")
	}
	allowed := strings.Fields("schema id started_ms duration_ms client method path status request_bytes response_body_bytes wire_bytes connection_requests rewrite_visits phases_ms request_headers response_headers upstream tls worker_sent worker_dropped")
	if schema == 2 {
		allowed = append(allowed, "native")
	}
	for k := range e {
		if !contains(allowed, k) {
			return nil, fmt.Errorf("unknown event field")
		}
	}
	for _, k := range []string{"id", "client", "method", "path"} {
		s, ok := e[k].(string)
		if !ok || utf8.RuneCountInString(s) > 256 {
			return nil, fmt.Errorf("invalid %s", k)
		}
	}
	if !idPattern.MatchString(e["id"].(string)) {
		return nil, fmt.Errorf("invalid id")
	}
	e["path"] = strings.SplitN(e["path"].(string), "?", 2)[0]
	for _, k := range integers {
		v, ok := integer(e[k])
		if !ok || v < 0 {
			return nil, fmt.Errorf("invalid %s", k)
		}
	}
	status, _ := integer(e["status"])
	if status < 100 || status > 599 {
		return nil, fmt.Errorf("invalid status")
	}
	started, _ := integer(e["started_ms"])
	delta := started - time.Now().UnixMilli()
	if delta < -86400000 || delta > 86400000 {
		return nil, fmt.Errorf("timestamp outside ingestion window")
	}
	if v := e["wire_bytes"]; v != nil && !wirePattern.MatchString(fmt.Sprint(v)) {
		return nil, fmt.Errorf("invalid wire bytes")
	}
	for k, names := range nested {
		m, err := object(e[k])
		if err != nil {
			return nil, err
		}
		for key, v := range m {
			if !contains(strings.Fields(names), key) {
				return nil, fmt.Errorf("unknown nested field")
			}
			if v == nil {
				continue
			}
			switch x := v.(type) {
			case string:
				if utf8.RuneCountInString(x) > 256 {
					return nil, fmt.Errorf("nested string too long")
				}
			case json.Number:
				if _, err := strconv.ParseInt(string(x), 10, 64); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("invalid nested value")
			}
		}
	}
	if schema == 2 {
		n, err := object(e["native"])
		if err != nil {
			return nil, err
		}
		if len(n) != len(spec.Scalars)+len(spec.Arrays) {
			return nil, fmt.Errorf("incomplete native schema")
		}
		for name, fields := range spec.Scalars {
			m, err := object(n[name])
			if err != nil {
				return nil, err
			}
			if err = checked(m, fields); err != nil {
				return nil, err
			}
			if name == "policy" {
				v := m["buffering_header"]
				if v != nil && v != "yes" && v != "no" && v != "other" {
					return nil, fmt.Errorf("invalid buffering header")
				}
			}
		}
		for name, s := range spec.Arrays {
			a, ok := n[name].([]any)
			if !ok || len(a) > s.Cap {
				return nil, fmt.Errorf("observation cap exceeded")
			}
			for _, v := range a {
				m, err := object(v)
				if err != nil {
					return nil, err
				}
				if err = checked(m, s.Fields); err != nil {
					return nil, err
				}
				if name == "trace" {
					event, _ := m["event"].(string)
					at, valid := integer(m["at_ms"])
					child, validChild := integer(m["subrequest"])
					if !contains(spec.Trace, event) || !valid || at < 0 || !validChild || child < 0 || child > 8 {
						return nil, fmt.Errorf("invalid trace")
					}
				} else if m["status"] != nil {
					v, _ := integer(m["status"])
					if v != 0 && (v < 100 || v > 599) {
						return nil, fmt.Errorf("invalid native status")
					}
				}
			}
		}
	}
	return e, nil
}
