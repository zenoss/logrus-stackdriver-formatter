package stackdriver

import (
	"bytes"
	"encoding/json"
	"reflect"
	"runtime"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/zenoss/logrus-stackdriver-formatter/test"
)

func TestStackSkip(t *testing.T) {
	var out bytes.Buffer

	logger := logrus.New()
	logger.Out = &out
	logger.Formatter = NewFormatter(
		WithService("test"),
		WithVersion("0.1"),
		WithStackSkip("github.com/zenoss/logrus-stackdriver-formatter/test"),
	)

	mylog := test.LogWrapper{
		Logger: logger,
	}

	_, _, logLine, _ := runtime.Caller(0)
	mylog.Error("my log entry") // must be on the line right after logLine

	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)

	want := map[string]any{
		"severity": "ERROR",
		"message":  "my log entry",
		"serviceContext": map[string]any{
			"service": "test",
			"version": "0.1",
		},
		"context": map[string]any{
			"reportLocation": map[string]any{
				"filePath":     "github.com/zenoss/logrus-stackdriver-formatter/stackskip_test.go",
				"lineNumber":   float64(logLine + 1),
				"functionName": "TestStackSkip",
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("unexpected output = %#v; want = %#v", got, want)
	}
}
