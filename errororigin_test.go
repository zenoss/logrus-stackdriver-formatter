package stackdriver

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/zenoss/logrus-stackdriver-formatter/test"
)

const testFilePath = "github.com/zenoss/logrus-stackdriver-formatter/errororigin_test.go"

// reportLocationOf runs log against a logger using f and returns the
// reportLocation of the entry it writes.
func reportLocationOf(t *testing.T, f *Formatter, log func(*logrus.Logger)) map[string]any {
	t.Helper()

	var out bytes.Buffer
	logger := logrus.New()
	logger.Out = &out
	logger.Formatter = f

	log(logger)

	var entry map[string]any
	err := json.Unmarshal(out.Bytes(), &entry)
	if err != nil {
		t.Fatalf("unmarshal log entry %q: %v", out.String(), err)
	}

	ctx, _ := entry["context"].(map[string]any)
	rl, ok := ctx["reportLocation"].(map[string]any)
	if !ok {
		t.Fatalf("entry has no reportLocation: %q", out.String())
	}
	return rl
}

func TestErrorOriginDeepStack(t *testing.T) {
	// Depths around the initial 32-entry buffer and its doublings are where the
	// growth loop is exercised, so cover every depth in that range plus larger ones.
	depths := []int{100, 1000}
	for d := 0; d <= 70; d++ {
		depths = append(depths, d)
	}

	for _, depth := range depths {
		f := NewFormatter(WithStackSkip("github.com/zenoss/logrus-stackdriver-formatter/test"))

		var wantLine int
		rl := reportLocationOf(t, f, func(logger *logrus.Logger) {
			wrapper := test.LogWrapper{Logger: logger}
			_, _, wantLine, _ = runtime.Caller(0)
			wrapper.ErrorAtDepth(depth, "deep") // line wantLine+1
		})

		if got := rl["filePath"]; got != testFilePath {
			t.Errorf("depth %d: filePath = %v; want %v", depth, got, testFilePath)
		}
		if got := rl["lineNumber"]; got != float64(wantLine+1) {
			t.Errorf("depth %d: lineNumber = %v; want %d", depth, got, wantLine+1)
		}
		if got, _ := rl["functionName"].(string); !strings.HasPrefix(got, "TestErrorOriginDeepStack") {
			t.Errorf("depth %d: functionName = %q; want prefix %q", depth, got, "TestErrorOriginDeepStack")
		}
	}
}

func TestErrorOriginAllSkipped(t *testing.T) {
	// Skip every package on the current stack, then ask errorOrigin directly.
	pcs := make([]uintptr, 1024)
	n := runtime.Callers(0, pcs)
	frames := runtime.CallersFrames(pcs[:n])

	f := NewFormatter()
	for {
		frame, more := frames.Next()
		if frame.Function != "" {
			f.StackSkip = append(f.StackSkip, packagePath(frame.Function))
		}
		if !more {
			break
		}
	}

	frame, ok := f.errorOrigin()
	if ok {
		t.Errorf("errorOrigin() = %q, true; want false when every frame is skipped", frame.Function)
	}
}

func TestErrorOriginClosure(t *testing.T) {
	f := NewFormatter()

	var wantLine int
	rl := reportLocationOf(t, f, func(logger *logrus.Logger) {
		_, _, wantLine, _ = runtime.Caller(0)
		logger.Error("closure") // line wantLine+1
	})

	if got := rl["filePath"]; got != testFilePath {
		t.Errorf("filePath = %v; want %v", got, testFilePath)
	}
	if got := rl["lineNumber"]; got != float64(wantLine+1) {
		t.Errorf("lineNumber = %v; want %d", got, wantLine+1)
	}
	if got, _ := rl["functionName"].(string); !strings.HasPrefix(got, "TestErrorOriginClosure.func") {
		t.Errorf("functionName = %q; want a closure of TestErrorOriginClosure", got)
	}
}

// originProbe logs from a pointer-receiver method.
type originProbe struct{}

func (*originProbe) logError(logger *logrus.Logger) {
	logger.Error("method")
}

func TestErrorOriginPointerMethod(t *testing.T) {
	f := NewFormatter()

	rl := reportLocationOf(t, f, func(logger *logrus.Logger) {
		(&originProbe{}).logError(logger)
	})

	if got := rl["filePath"]; got != testFilePath {
		t.Errorf("filePath = %v; want %v", got, testFilePath)
	}
	if got := rl["functionName"]; got != "(*originProbe).logError" {
		t.Errorf("functionName = %v; want %q", got, "(*originProbe).logError")
	}
}

func TestPackagePath(t *testing.T) {
	tests := []struct {
		name     string
		function string
		want     string
	}{
		{"plain function", "github.com/a/b.F", "github.com/a/b"},
		{"pointer method", "github.com/a/b.(*T).M", "github.com/a/b"},
		{"closure", "github.com/a/b.init.func2", "github.com/a/b"},
		{"main", "main.main", "main"},
		{"generic", "github.com/a/b.G[...]", "github.com/a/b"},
		{"no package", "F", "F"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := packagePath(tt.function); got != tt.want {
				t.Errorf("packagePath(%q) = %q; want %q", tt.function, got, tt.want)
			}
		})
	}
}

func TestFunctionName(t *testing.T) {
	tests := []struct {
		name     string
		function string
		want     string
	}{
		{"plain function", "github.com/a/b.F", "F"},
		{"pointer method", "github.com/a/b.(*T).M", "(*T).M"},
		{"closure", "github.com/a/b.init.func2", "init.func2"},
		{"main", "main.main", "main"},
		{"generic", "github.com/a/b.G[...]", "G[...]"},
		{"no package", "F", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := functionName(tt.function); got != tt.want {
				t.Errorf("functionName(%q) = %q; want %q", tt.function, got, tt.want)
			}
		})
	}
}
