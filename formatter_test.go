package stackdriver

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

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
