package stackdriver

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/sirupsen/logrus"
)

func TestFormatter(t *testing.T) {
	skipTimestamp = true

	for _, tt := range formatterTests {
		var out bytes.Buffer

		logger := logrus.New()
		logger.Out = &out
		logger.Formatter = NewFormatter(
			WithService("test"),
			WithVersion("0.1"),
		)

		tt.run(logger)

		var got map[string]any
		_ = json.Unmarshal(out.Bytes(), &got)

		dropVolatileReportLocation(t, got)

		if !reflect.DeepEqual(got, tt.out) {
			t.Errorf("unexpected output = %#v; want = %#v", got, tt.out)
		}
	}
}

// dropVolatileReportLocation verifies the lineNumber and functionName in the
// reportLocation of entry, then removes them so the rest of the entry can be
// compared exactly. The function name is a closure name whose exact form
// depends on the Go toolchain ("init.func2" or "glob..func2"), so it is only
// checked to contain "func". The line number must be positive.
func dropVolatileReportLocation(t *testing.T, entry map[string]any) {
	t.Helper()

	ctx, _ := entry["context"].(map[string]any)
	rl, ok := ctx["reportLocation"].(map[string]any)
	if !ok {
		return
	}

	fn, _ := rl["functionName"].(string)
	if !strings.Contains(fn, "func") {
		t.Errorf("reportLocation.functionName = %q; want it to contain %q", fn, "func")
	}

	if line, _ := rl["lineNumber"].(float64); line < 1 {
		t.Errorf("reportLocation.lineNumber = %v; want a positive line number", line)
	}

	delete(rl, "lineNumber")
	delete(rl, "functionName")
}

var formatterTests = []struct {
	run func(*logrus.Logger)
	out map[string]any
}{
	{
		run: func(logger *logrus.Logger) {
			logger.WithField("foo", "bar").Info("my log entry")
		},
		out: map[string]any{
			"severity": "INFO",
			"message":  "my log entry",
			"context": map[string]any{
				"data": map[string]any{
					"foo": "bar",
				},
			},
		},
	},
	{
		run: func(logger *logrus.Logger) {
			logger.WithField("foo", "bar").Error("my log entry")
		},
		out: map[string]any{
			"severity": "ERROR",
			"message":  "my log entry",
			"serviceContext": map[string]any{
				"service": "test",
				"version": "0.1",
			},
			"context": map[string]any{
				"data": map[string]any{
					"foo": "bar",
				},
				"reportLocation": map[string]any{
					"filePath": "github.com/zenoss/logrus-stackdriver-formatter/formatter_test.go",
				},
			},
		},
	},
	{
		run: func(logger *logrus.Logger) {
			logger.
				WithField("foo", "bar").
				WithError(errors.New("test error")).
				Error("my log entry")
		},
		out: map[string]any{
			"severity": "ERROR",
			"message":  "my log entry: test error",
			"serviceContext": map[string]any{
				"service": "test",
				"version": "0.1",
			},
			"context": map[string]any{
				"data": map[string]any{
					"foo": "bar",
				},
				"reportLocation": map[string]any{
					"filePath": "github.com/zenoss/logrus-stackdriver-formatter/formatter_test.go",
				},
			},
		},
	},
	{
		run: func(logger *logrus.Logger) {
			logger.
				WithFields(logrus.Fields{
					"foo": "bar",
					"httpRequest": map[string]any{
						"method": "GET",
					},
				}).
				Error("my log entry")
		},
		out: map[string]any{
			"severity": "ERROR",
			"message":  "my log entry",
			"serviceContext": map[string]any{
				"service": "test",
				"version": "0.1",
			},
			"context": map[string]any{
				"data": map[string]any{
					"foo": "bar",
				},
				"httpRequest": map[string]any{
					"method": "GET",
				},
				"reportLocation": map[string]any{
					"filePath": "github.com/zenoss/logrus-stackdriver-formatter/formatter_test.go",
				},
			},
		},
	},
}

func TestFormatterInvalidUTF8(t *testing.T) {
	skipTimestamp = true

	f := NewFormatter()
	entry := &logrus.Entry{
		Level:   logrus.InfoLevel,
		Message: "bad \xff byte",
		Data:    logrus.Fields{"k\xfe": "v\xff\xff", "nested": []any{"\xfa"}},
	}

	b, err := f.Format(entry)
	if err != nil {
		t.Fatalf("Format() error = %v; want the entry written with U+FFFD", err)
	}
	if !utf8.Valid(b) {
		t.Errorf("output %q is not valid UTF-8", b)
	}
	if !jsontext.Value(bytes.TrimSpace(b)).IsValid() {
		t.Errorf("output %q is not a valid JSON value", b)
	}

	var got map[string]any
	err = json.Unmarshal(b, &got)
	if err != nil {
		t.Fatalf("unmarshal %q: %v", b, err)
	}
	if got["message"] != "bad � byte" {
		t.Errorf("message = %q; want %q", got["message"], "bad � byte")
	}
	ctx := got["context"].(map[string]any)["data"].(map[string]any)
	if ctx["k�"] != "v��" {
		t.Errorf("data = %v; want key and value with one U+FFFD per invalid byte", ctx)
	}
}

func TestFormatterDeterministicKeys(t *testing.T) {
	skipTimestamp = true

	f := NewFormatter()
	entry := &logrus.Entry{
		Level:   logrus.InfoLevel,
		Message: "keys",
		Data:    logrus.Fields{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8},
	}

	first, err := f.Format(entry)
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}
	for i := range 50 {
		again, err := f.Format(entry)
		if err != nil {
			t.Fatalf("Format() run %d error = %v", i, err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d output %q differs from %q", i, again, first)
		}
	}
}

type marshalPoint struct {
	X int `json:"x"`
	y int
}

func TestFormatterCommonTypes(t *testing.T) {
	skipTimestamp = true

	ts := time.Date(2024, time.March, 15, 10, 30, 0, 123456789, time.UTC)
	kyiv := time.FixedZone("EET", 2*60*60)

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "time.Duration", value: 90 * time.Second, want: `90000000000`},
		{name: "time.Duration zero", value: time.Duration(0), want: `0`},
		{name: "time.Duration negative", value: -1500 * time.Millisecond, want: `-1500000000`},
		{name: "*time.Duration", value: new(5 * time.Nanosecond), want: `5`},
		{name: "*time.Duration nil", value: (*time.Duration)(nil), want: `null`},
		{name: "[]time.Duration", value: []time.Duration{time.Second, time.Millisecond}, want: `[1000000000,1000000]`},
		{name: "struct with time.Duration", value: struct {
			Timeout time.Duration `json:"timeout"`
		}{Timeout: time.Minute}, want: `{"timeout":60000000000}`},
		{name: "time.Time UTC", value: ts, want: `"2024-03-15T10:30:00.123456789Z"`},
		{name: "time.Time offset", value: ts.In(kyiv), want: `"2024-03-15T12:30:00.123456789+02:00"`},
		{name: "time.Time zero", value: time.Time{}, want: `"0001-01-01T00:00:00Z"`},
		{name: "*time.Time", value: &ts, want: `"2024-03-15T10:30:00.123456789Z"`},
		{name: "*time.Time nil", value: (*time.Time)(nil), want: `null`},
		{name: "time.Month", value: time.March, want: `3`},
		{name: "time.Weekday", value: time.Friday, want: `5`},
		{name: "string", value: "bar", want: `"bar"`},
		{name: "bool", value: true, want: `true`},
		{name: "int", value: -42, want: `-42`},
		{name: "int64 max", value: int64(math.MaxInt64), want: `9223372036854775807`},
		{name: "uint64 max", value: uint64(math.MaxUint64), want: `18446744073709551615`},
		{name: "float64", value: 1.5, want: `1.5`},
		{name: "float32", value: float32(0.25), want: `0.25`},
		{name: "rune", value: 'x', want: `120`},
		{name: "nil", value: nil, want: `null`},
		{name: "[]byte", value: []byte("hi"), want: `"aGk="`},
		{name: "[]string", value: []string{"a", "b"}, want: `["a","b"]`},
		{name: "[]string nil", value: []string(nil), want: `[]`},
		{name: "[2]int", value: [2]int{1, 2}, want: `[1,2]`},
		{name: "map[string]int", value: map[string]int{"b": 2, "a": 1}, want: `{"a":1,"b":2}`},
		{name: "map[string]int nil", value: map[string]int(nil), want: `{}`},
		{name: "map[int]string", value: map[int]string{1: "a"}, want: `{"1":"a"}`},
		{name: "struct", value: marshalPoint{X: 1, y: 2}, want: `{"x":1}`},
		{name: "*struct nil", value: (*marshalPoint)(nil), want: `null`},
		{name: "error", value: errors.New("boom"), want: `"boom"`},
		{name: "wrapped error", value: fmt.Errorf("op: %w", errors.New("boom")), want: `"op: boom"`},
		{name: "jsontext.Value", value: jsontext.Value(`{"a":1}`), want: `{"a":1}`},
		{name: "net.IP", value: net.ParseIP("10.0.0.1"), want: `"10.0.0.1"`},
		{name: "netip.Addr", value: netip.MustParseAddr("2001:db8::1"), want: `"2001:db8::1"`},
		{name: "*big.Int", value: big.NewInt(42), want: `42`},
		{name: "complex128", value: complex(1, 2), want: `"(1+2i)"`},
		{name: "complex64", value: complex64(complex(1, 2)), want: `"(1+2i)"`},
		{name: "float64 NaN", value: math.NaN(), want: `"NaN"`},
		{name: "float64 +Inf", value: math.Inf(1), want: `"+Inf"`},
		{name: "float64 -Inf", value: math.Inf(-1), want: `"-Inf"`},
		{name: "float32 NaN", value: float32(math.NaN()), want: `"NaN"`},
		{name: "float32 -Inf", value: float32(math.Inf(-1)), want: `"-Inf"`},
		{name: "unsafe.Pointer nil", value: unsafe.Pointer(nil), want: `null`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDataValue(t, tt.value)
			if got != tt.want {
				t.Errorf("context.data.v = %s; want %s", got, tt.want)
			}
		})
	}
}

// TestFormatterUnsupportedTypes checks that a value encoding/json/v2 cannot
// marshal is dropped, the rest of the record is still written, and the dropped
// field is listed in unsupportedFields.
func TestFormatterUnsupportedTypes(t *testing.T) {
	skipTimestamp = true

	tests := []struct {
		name     string
		value    any
		wantType string
	}{
		{name: "chan", value: make(chan int), wantType: "chan int"},
		{name: "func", value: func() {}, wantType: "func()"},
		{name: "struct with chan field", value: struct{ C chan int }{C: make(chan int)}, wantType: "chan int"},
		{name: "struct with only unexported fields", value: struct{ y int }{y: 1}, wantType: "struct { y int }"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRecord(t, logrus.Fields{"bad": tt.value, "ok": "fine"})

			if _, ok := got.Context.Data["bad"]; ok {
				t.Errorf("context.data.bad = %s; want it dropped", got.Context.Data["bad"])
			}
			if v := string(got.Context.Data["ok"]); v != `"fine"` {
				t.Errorf("context.data.ok = %s; want %q", v, "fine")
			}
			if g := got.Context.UnsupportedFields["bad"]; g != tt.wantType {
				t.Errorf("unsupportedFields.bad = %q; want %q", g, tt.wantType)
			}
		})
	}
}

// TestFormatterUnsupportedFieldLimit checks that up to maxUnsupportedFields
// fields are dropped, and a record with more unsupported fields fails.
func TestFormatterUnsupportedFieldLimit(t *testing.T) {
	skipTimestamp = true

	fields := func(n int) logrus.Fields {
		f := logrus.Fields{"ok": "fine"}
		for i := range n {
			f[fmt.Sprintf("bad%d", i)] = make(chan int)
		}
		return f
	}

	got := formatRecord(t, fields(maxUnsupportedFields))
	if len(got.Context.UnsupportedFields) != maxUnsupportedFields {
		t.Errorf("unsupportedFields has %d entries; want %d", len(got.Context.UnsupportedFields), maxUnsupportedFields)
	}

	_, err := NewFormatter().Format(&logrus.Entry{
		Level:   logrus.InfoLevel,
		Message: "too many",
		Data:    fields(maxUnsupportedFields + 1),
	})
	if err == nil {
		t.Fatalf("Format() with %d unsupported fields = nil error; want an error", maxUnsupportedFields+1)
	}
}

// TestFormatterUnsupportedOutsideData checks that an unsupported value outside
// the data fields still fails the record.
func TestFormatterUnsupportedOutsideData(t *testing.T) {
	skipTimestamp = true

	_, err := NewFormatter().Format(&logrus.Entry{
		Level:   logrus.ErrorLevel,
		Message: "request",
		Data: logrus.Fields{
			"httpRequest": map[string]any{"bad": make(chan int)},
		},
	})
	if err == nil {
		t.Fatal("Format() = nil error; want an error for an unsupported httpRequest value")
	}
}

// decodedRecord is the part of a formatted record the tests check.
type decodedRecord struct {
	Message string `json:"message"`
	Context struct {
		Data              map[string]jsontext.Value `json:"data"`
		UnsupportedFields map[string]string         `json:"unsupportedFields"`
	} `json:"context"`
}

// formatRecord formats an info entry with the given fields and decodes it.
func formatRecord(t *testing.T, fields logrus.Fields) decodedRecord {
	t.Helper()

	b, err := NewFormatter().Format(&logrus.Entry{
		Level:   logrus.InfoLevel,
		Message: "record",
		Data:    fields,
	})
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	var got decodedRecord
	err = json.Unmarshal(b, &got)
	if err != nil {
		t.Fatalf("unmarshal %q: %v", b, err)
	}
	return got
}

// formatDataValue formats an info entry with value in the "v" field and
// returns the raw JSON written for it under context.data.
func formatDataValue(t *testing.T, value any) string {
	t.Helper()

	b, err := NewFormatter().Format(&logrus.Entry{
		Level:   logrus.InfoLevel,
		Message: "types",
		Data:    logrus.Fields{"v": value},
	})
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	var got struct {
		Context struct {
			Data map[string]jsontext.Value `json:"data"`
		} `json:"context"`
	}
	err = json.Unmarshal(b, &got)
	if err != nil {
		t.Fatalf("unmarshal %q: %v", b, err)
	}

	v, ok := got.Context.Data["v"]
	if !ok {
		t.Fatalf("output %q has no context.data.v", b)
	}
	return string(v)
}

// TestFormatterNoExportedFields checks that errors and locations nested in a
// value, which encoding/json/v2 cannot marshal as structs, are written by name
// instead of dropping the log entry.
func TestFormatterNoExportedFields(t *testing.T) {
	skipTimestamp = true

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "[]error", value: []error{errors.New("a")}, want: `["a"]`},
		{name: "[]error nil", value: []error(nil), want: `[]`},
		{name: "struct with error field", value: struct {
			Err error
		}{Err: errors.New("a")}, want: `{"Err":"a"}`},
		{name: "map[string]error", value: map[string]error{"k": errors.New("a")}, want: `{"k":"a"}`},
		{name: "*time.Location", value: time.UTC, want: `"UTC"`},
		{name: "empty struct", value: struct{}{}, want: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDataValue(t, tt.value)
			if got != tt.want {
				t.Errorf("context.data.v = %s; want %s", got, tt.want)
			}
		})
	}
}

// TestFormatterNoExportedFieldsKeepsOtherFields checks that the other fields of
// an entry are still written when one of them holds a nested error.
func TestFormatterNoExportedFieldsKeepsOtherFields(t *testing.T) {
	skipTimestamp = true

	b, err := NewFormatter().Format(&logrus.Entry{
		Level:   logrus.InfoLevel,
		Message: "keep",
		Data: logrus.Fields{
			"bad":  []error{errors.New("a")},
			"ok":   "fine",
			"n":    42,
			"when": time.Second,
		},
	})
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	var got struct {
		Message string `json:"message"`
		Context struct {
			Data map[string]jsontext.Value `json:"data"`
		} `json:"context"`
	}
	err = json.Unmarshal(b, &got)
	if err != nil {
		t.Fatalf("unmarshal %q: %v", b, err)
	}

	want := map[string]string{
		"bad":  `["a"]`,
		"ok":   `"fine"`,
		"n":    `42`,
		"when": `1000000000`,
	}
	if got.Message != "keep" {
		t.Errorf("message = %q; want %q", got.Message, "keep")
	}
	for k, w := range want {
		if v := string(got.Context.Data[k]); v != w {
			t.Errorf("context.data.%s = %s; want %s", k, v, w)
		}
	}
}

// TestFormatterErrorFieldInMessage checks that an error under the "error" key
// is appended to the message and removed from the data.
func TestFormatterErrorFieldInMessage(t *testing.T) {
	skipTimestamp = true

	b, err := NewFormatter().Format(&logrus.Entry{
		Level:   logrus.ErrorLevel,
		Message: "save failed",
		Data:    logrus.Fields{"error": errors.New("boom"), "id": 7},
	})
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	var got struct {
		Message string `json:"message"`
		Context struct {
			Data map[string]jsontext.Value `json:"data"`
		} `json:"context"`
	}
	err = json.Unmarshal(b, &got)
	if err != nil {
		t.Fatalf("unmarshal %q: %v", b, err)
	}

	if want := "save failed: boom"; got.Message != want {
		t.Errorf("message = %q; want %q", got.Message, want)
	}
	if _, ok := got.Context.Data["error"]; ok {
		t.Errorf("context.data still has the error field: %s", b)
	}
	if v := string(got.Context.Data["id"]); v != `7` {
		t.Errorf("context.data.id = %s; want 7", v)
	}
}

// TestFormatterDoesNotModifyEntryData checks that Format leaves the entry's
// fields as they were, since hooks and other formatters read them too.
func TestFormatterDoesNotModifyEntryData(t *testing.T) {
	skipTimestamp = true

	data := logrus.Fields{
		"error":       errors.New("boom"),
		"httpRequest": map[string]any{"method": "GET"},
		"id":          7,
	}
	_, err := NewFormatter().Format(&logrus.Entry{
		Level:   logrus.ErrorLevel,
		Message: "save failed",
		Data:    data,
	})
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	if len(data) != 3 {
		t.Errorf("entry data has %d fields after Format(); want 3: %v", len(data), data)
	}
}

// TestFormatterUnsupportedFieldEscapedKey checks that a dropped field whose
// name needs JSON Pointer escaping is reported under its real name.
func TestFormatterUnsupportedFieldEscapedKey(t *testing.T) {
	skipTimestamp = true

	got := formatRecord(t, logrus.Fields{
		"a/b~c": struct{ C chan int }{C: make(chan int)},
		"ok":    "fine",
	})

	if _, ok := got.Context.Data["a/b~c"]; ok {
		t.Errorf("context.data has the dropped field: %s", got.Context.Data["a/b~c"])
	}
	if g := got.Context.UnsupportedFields["a/b~c"]; g != "chan int" {
		t.Errorf("unsupportedFields[%q] = %q; want %q", "a/b~c", g, "chan int")
	}
}
