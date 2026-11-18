package stackdriver

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

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

		var got map[string]interface{}
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
func dropVolatileReportLocation(t *testing.T, entry map[string]interface{}) {
	t.Helper()

	ctx, _ := entry["context"].(map[string]interface{})
	rl, ok := ctx["reportLocation"].(map[string]interface{})
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
	out map[string]interface{}
}{
	{
		run: func(logger *logrus.Logger) {
			logger.WithField("foo", "bar").Info("my log entry")
		},
		out: map[string]interface{}{
			"severity": "INFO",
			"message":  "my log entry",
			"context": map[string]interface{}{
				"data": map[string]interface{}{
					"foo": "bar",
				},
			},
		},
	},
	{
		run: func(logger *logrus.Logger) {
			logger.WithField("foo", "bar").Error("my log entry")
		},
		out: map[string]interface{}{
			"severity": "ERROR",
			"message":  "my log entry",
			"serviceContext": map[string]interface{}{
				"service": "test",
				"version": "0.1",
			},
			"context": map[string]interface{}{
				"data": map[string]interface{}{
					"foo": "bar",
				},
				"reportLocation": map[string]interface{}{
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
		out: map[string]interface{}{
			"severity": "ERROR",
			"message":  "my log entry: test error",
			"serviceContext": map[string]interface{}{
				"service": "test",
				"version": "0.1",
			},
			"context": map[string]interface{}{
				"data": map[string]interface{}{
					"foo": "bar",
				},
				"reportLocation": map[string]interface{}{
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
					"httpRequest": map[string]interface{}{
						"method": "GET",
					},
				}).
				Error("my log entry")
		},
		out: map[string]interface{}{
			"severity": "ERROR",
			"message":  "my log entry",
			"serviceContext": map[string]interface{}{
				"service": "test",
				"version": "0.1",
			},
			"context": map[string]interface{}{
				"data": map[string]interface{}{
					"foo": "bar",
				},
				"httpRequest": map[string]interface{}{
					"method": "GET",
				},
				"reportLocation": map[string]interface{}{
					"filePath": "github.com/zenoss/logrus-stackdriver-formatter/formatter_test.go",
				},
			},
		},
	},
}
