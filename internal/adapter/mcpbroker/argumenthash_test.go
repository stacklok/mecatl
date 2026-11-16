package mcpbroker

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The expected canonical strings in testdata/argumenthash/vectors.json come
// from RFC 8785 and hand-checked cases, cross-checked against ECMAScript's
// JSON.parse and JSON.stringify with sorted keys; sha256 is computed over the
// expected string, not by this package.
func TestArgumentsSHA256Vectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "argumenthash", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Vectors []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Canonical string          `json:"canonical"`
			SHA256    string          `json:"sha256"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			sent, sum, err := argumentsSHA256(v.Arguments)
			if err != nil {
				t.Fatalf("argumentsSHA256(%s): %v", v.Arguments, err)
			}
			if sum != v.SHA256 {
				t.Errorf("sum = %s, want %s", sum, v.SHA256)
			}
			wantSent := string(v.Arguments)
			if len(v.Arguments) == 0 || string(v.Arguments) == "null" {
				wantSent = "{}"
			}
			if string(sent) != wantSent {
				t.Errorf("sent = %s, want %s", sent, wantSent)
			}
			if _, canonical, err := canonicalArguments(v.Arguments); err != nil || canonical != v.Canonical {
				t.Errorf("canonical = %s, %v; want %s", canonical, err, v.Canonical)
			}
		})
	}
}

// TestFormatES6AppendixB checks number formatting against every sample in
// RFC 8785 Appendix B, including those argumentsSHA256 refuses because they
// are integers beyond 2^53 or not finite.
func TestFormatES6AppendixB(t *testing.T) {
	for _, c := range []struct{ ieee, want string }{
		{"0000000000000000", "0"},
		{"8000000000000000", "0"},
		{"0000000000000001", "5e-324"},
		{"8000000000000001", "-5e-324"},
		{"7fefffffffffffff", "1.7976931348623157e+308"},
		{"ffefffffffffffff", "-1.7976931348623157e+308"},
		{"4340000000000000", "9007199254740992"},
		{"c340000000000000", "-9007199254740992"},
		{"4430000000000000", "295147905179352830000"},
		{"44b52d02c7e14af5", "9.999999999999997e+22"},
		{"44b52d02c7e14af6", "1e+23"},
		{"44b52d02c7e14af7", "1.0000000000000001e+23"},
		{"444b1ae4d6e2ef4e", "999999999999999700000"},
		{"444b1ae4d6e2ef4f", "999999999999999900000"},
		{"444b1ae4d6e2ef50", "1e+21"},
		{"3eb0c6f7a0b5ed8c", "9.999999999999997e-7"},
		{"3eb0c6f7a0b5ed8d", "0.000001"},
		{"41b3de4355555553", "333333333.3333332"},
		{"41b3de4355555554", "333333333.33333325"},
		{"41b3de4355555555", "333333333.3333333"},
		{"41b3de4355555556", "333333333.3333334"},
		{"41b3de4355555557", "333333333.33333343"},
		{"becbf647612f3696", "-0.0000033333333333333333"},
		{"43143ff3c1cb0959", "1424953923781206.2"},
	} {
		bits, err := strconv.ParseUint(c.ieee, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := formatES6(math.Float64frombits(bits)); got != c.want {
			t.Errorf("formatES6(%s) = %s, want %s", c.ieee, got, c.want)
		}
	}
}

func TestArgumentsSHA256RefusesOutsideIJSON(t *testing.T) {
	for name, args := range map[string]string{
		"2^53+1":                           `{"n":9007199254740993}`,
		"-(2^53+1)":                        `{"n":-9007199254740993}`,
		"2^53+1 in exponent form":          `{"n":9.007199254740993e15}`,
		"2^53+0.5 rounding to 2^53":        `{"n":9007199254740992.5}`,
		"2^53+2":                           `{"n":9007199254740994}`,
		"1e21 (Appendix B)":                `{"n":1e+21}`,
		"1e23 (Appendix B)":                `{"n":1e+23}`,
		"295147905179352830000":            `{"n":295147905179352830000}`,
		"largest double (Appendix B)":      `{"n":1.7976931348623157e+308}`,
		"nested beyond 2^53":               `{"a":[{"n":-1e300}]}`,
		"1e400":                            `{"n":1e400}`,
		"-1e400":                           `{"n":-1e400}`,
		"RFC 8785 3.2.4 example with 1E30": `{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001],"literals":[null,true,false]}`,
		"invalid UTF-8 in value":           "{\"s\":\"\xff\"}",
		"invalid UTF-8 in key":             "{\"\xc3\x28\":1}",
		"truncated UTF-8":                  "{\"s\":\"\xe2\x82\"}",
		"UTF-8 encoded surrogate":          "{\"s\":\"\xed\xa0\x80\"}",
		"lone \\ud800":                     `{"s":"\ud800"}`,
		"lone \\udc00":                     `{"s":"\udc00"}`,
		"lone \\ud800 in key":              `{"\ud800":1}`,
		"\\ud800 then a letter":            `{"s":"\ud800A"}`,
		"\\ud800 then \\u0041":             `{"s":"\ud800\u0041"}`,
		"two high surrogates":              `{"s":"\ud800\ud800"}`,
		"reversed pair":                    `{"s":"\ude00\ud83d"}`,
		"\\ud800 after escaped backslash":  `{"s":"\\\ud800"}`,
		"duplicate keys":                   `{"a":1,"a":2}`,
		"duplicate keys by escape":         `{"a":1,"\u0061":2}`,
		"nested duplicate keys":            `{"o":{"k":1,"k":1}}`,
		"top-level array":                  `[1,2]`,
		"top-level string":                 `"x"`,
		"top-level number":                 `1`,
		"top-level true":                   `true`,
		"malformed":                        `{"a":1`,
		"trailing value":                   `{}{}`,
		"trailing garbage":                 `{} x`,
	} {
		t.Run(name, func(t *testing.T) {
			sent, sum, err := argumentsSHA256(json.RawMessage(args))
			if !errors.Is(err, errNotIJSON) {
				t.Fatalf("argumentsSHA256(%q) = %s, %q, %v; want errNotIJSON", args, sent, sum, err)
			}
			if sent != nil || sum != "" {
				t.Errorf("refusal returned sent %q, sum %q", sent, sum)
			}
		})
	}
}
