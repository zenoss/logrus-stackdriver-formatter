package stackdriver

import (
	"encoding/json"
	"fmt"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

var skipTimestamp bool

type severity string

const (
	severityDebug    severity = "DEBUG"
	severityInfo     severity = "INFO"
	severityWarning  severity = "WARNING"
	severityError    severity = "ERROR"
	severityCritical severity = "CRITICAL"
	severityAlert    severity = "ALERT"
)

var levelsToSeverity = map[logrus.Level]severity{
	logrus.DebugLevel: severityDebug,
	logrus.InfoLevel:  severityInfo,
	logrus.WarnLevel:  severityWarning,
	logrus.ErrorLevel: severityError,
	logrus.FatalLevel: severityCritical,
	logrus.PanicLevel: severityAlert,
}

type serviceContext struct {
	Service string `json:"service,omitempty"`
	Version string `json:"version,omitempty"`
}

type reportLocation struct {
	FilePath     string `json:"filePath,omitempty"`
	LineNumber   int    `json:"lineNumber,omitempty"`
	FunctionName string `json:"functionName,omitempty"`
}

type context struct {
	Data           map[string]any  `json:"data,omitempty"`
	ReportLocation *reportLocation `json:"reportLocation,omitempty"`
	HTTPRequest    map[string]any  `json:"httpRequest,omitempty"`
}

type entry struct {
	Timestamp      string          `json:"timestamp,omitempty"`
	ServiceContext *serviceContext `json:"serviceContext,omitempty"`
	Message        string          `json:"message,omitempty"`
	Severity       severity        `json:"severity,omitempty"`
	Context        *context        `json:"context,omitempty"`
}

// Formatter implements Stackdriver formatting for logrus.
type Formatter struct {
	Service   string
	Version   string
	StackSkip []string
}

// Option lets you configure the Formatter.
type Option func(*Formatter)

// WithService lets you configure the service name used for error reporting.
func WithService(n string) Option {
	return func(f *Formatter) {
		f.Service = n
	}
}

// WithVersion lets you configure the service version used for error reporting.
func WithVersion(v string) Option {
	return func(f *Formatter) {
		f.Version = v
	}
}

// WithStackSkip lets you configure which packages should be skipped for locating the error.
func WithStackSkip(v string) Option {
	return func(f *Formatter) {
		f.StackSkip = append(f.StackSkip, v)
	}
}

// NewFormatter returns a new Formatter.
func NewFormatter(options ...Option) *Formatter {
	fmtr := Formatter{
		StackSkip: []string{
			"github.com/sirupsen/logrus",
		},
	}
	for _, option := range options {
		option(&fmtr)
	}
	return &fmtr
}

// errorOrigin returns the first frame above Format whose package is not in
// f.StackSkip. The boolean is false when every frame is skipped.
func (f *Formatter) errorOrigin() (runtime.Frame, bool) {
	skip := func(pkg string) bool {
		return slices.Contains(f.StackSkip, pkg)
	}

	// Start with room for 32 program counters and double the buffer until the
	// whole stack fits, so deep stacks are still walked to the end.
	pcs := make([]uintptr, 32)
	for {
		// Skip runtime.Callers, errorOrigin and Format.
		n := runtime.Callers(3, pcs)
		frames := runtime.CallersFrames(pcs[:n])
		for {
			frame, more := frames.Next()
			if frame.Function == "" {
				break
			}
			pkg := packagePath(frame.Function)
			// Remove vendoring from package path. This only applies in GOPATH
			// mode (GO111MODULE=off), where vendored packages keep their
			// "<root>/vendor/" prefix in runtime function names, so
			// "example.com/app/vendor/lib" must match a StackSkip entry of
			// "lib". In module mode, a vendored package's function name is
			// already its import path, so this is a no-op.
			parts := strings.SplitN(pkg, "/vendor/", 2)
			pkg = parts[len(parts)-1]
			if !skip(pkg) {
				return frame, true
			}
			if !more {
				break
			}
		}
		if n < len(pcs) {
			return runtime.Frame{}, false
		}
		pcs = make([]uintptr, 2*len(pcs))
	}
}

// packagePath returns the import path of the package that declares function,
// which is the form runtime.Frame.Function uses, for example
// "github.com/a/b" for "github.com/a/b.(*T).M". Type arguments of generic
// functions are ignored, since they may contain slashes.
func packagePath(function string) string {
	if i := strings.IndexByte(function, '['); i >= 0 {
		function = function[:i]
	}
	start := strings.LastIndexByte(function, '/') + 1
	if dot := strings.IndexByte(function[start:], '.'); dot >= 0 {
		return function[:start+dot]
	}
	return function
}

// functionName returns function without its package path, for example
// "init.func2" for "github.com/a/b.init.func2".
func functionName(function string) string {
	pkg := packagePath(function)
	if len(pkg) >= len(function) {
		return ""
	}
	return function[len(pkg)+1:]
}

// Format formats a logrus entry according to the Stackdriver specifications.
func (f *Formatter) Format(e *logrus.Entry) ([]byte, error) {
	severity := levelsToSeverity[e.Level]

	data := make(map[string]any, len(e.Data))
	for k, v := range e.Data {
		switch t := v.(type) {
		case error:
			data[k] = t.Error()
		default:
			data[k] = v
		}
	}

	ee := entry{
		Message:  e.Message,
		Severity: severity,
		Context: &context{
			Data: data,
		},
	}

	if !skipTimestamp {
		ee.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	switch severity {
	case severityError, severityCritical, severityAlert:
		ee.ServiceContext = &serviceContext{
			Service: f.Service,
			Version: f.Version,
		}

		// When using WithError(), the error is sent separately, but Error
		// Reporting expects it to be a part of the message so we append it
		// instead.
		if err, ok := data["error"]; ok {
			ee.Message = fmt.Sprintf("%s: %s", e.Message, err)
			delete(data, "error")
		} else {
			ee.Message = e.Message
		}

		// As a convenience, when using supplying the httpRequest field, it
		// gets special care.
		if reqData, ok := data["httpRequest"]; ok {
			if req, ok := reqData.(map[string]any); ok {
				ee.Context.HTTPRequest = req
				delete(data, "httpRequest")
			}
		}

		// Extract report location from call stack. External test packages
		// ("pkg_test") are reported under the package they test.
		if frame, ok := f.errorOrigin(); ok {
			ee.Context.ReportLocation = &reportLocation{
				FilePath:     strings.TrimSuffix(packagePath(frame.Function), "_test") + "/" + path.Base(frame.File),
				LineNumber:   frame.Line,
				FunctionName: functionName(frame.Function),
			}
		}
	}

	b, err := json.Marshal(ee)
	if err != nil {
		return nil, fmt.Errorf("marshal log entry: %w", err)
	}

	return append(b, '\n'), nil
}
