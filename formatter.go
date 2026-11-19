package stackdriver

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"math"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"
	"unsafe"

	"github.com/sirupsen/logrus"
)

var skipTimestamp bool

// marshalOptions are the encoding/json/v2 options used to write log entries.
// Map keys are sorted so output is stable, and invalid UTF-8 is replaced
// with U+FFFD instead of failing the entry.
//
// encoding/json/v2 has no default representation for time.Duration and
// fails to marshal it, which would drop the whole log entry. Durations are
// written as integer nanoseconds, as encoding/json (v1) did, so existing
// log consumers keep working.
var marshalOptions = json.JoinOptions(
	json.Deterministic(true),
	jsontext.AllowInvalidUTF8(true),
	jsonv1.FormatDurationAsNano(true),
	json.WithMarshalers(logMarshalers),
)

// maxUnsupportedFields bounds how many fields Format drops from one record
// because they cannot be marshaled.
const maxUnsupportedFields = 8

// logMarshalers write values that encoding/json/v2 rejects, so one such value
// does not fail the whole record. Each entry handles one type; chan and func
// types have no finite list, so they are dropped by Format instead.
var logMarshalers = json.JoinMarshalers(
	// Errors are written as their message, as Format already does for
	// top-level errors.
	json.MarshalToFunc(func(enc *jsontext.Encoder, err error) error {
		return enc.WriteToken(jsontext.String(err.Error()))
	}),
	json.MarshalToFunc(func(enc *jsontext.Encoder, loc *time.Location) error {
		return enc.WriteToken(jsontext.String(loc.String()))
	}),
	// Non-finite floats are written as strings. Other floats fall through
	// to the default encoding.
	json.MarshalToFunc(func(enc *jsontext.Encoder, f float64) error {
		return marshalNonFinite(enc, f)
	}),
	json.MarshalToFunc(func(enc *jsontext.Encoder, f float32) error {
		return marshalNonFinite(enc, float64(f))
	}),
	json.MarshalToFunc(func(enc *jsontext.Encoder, c complex128) error {
		return enc.WriteToken(jsontext.String(fmt.Sprint(c)))
	}),
	json.MarshalToFunc(func(enc *jsontext.Encoder, c complex64) error {
		return enc.WriteToken(jsontext.String(fmt.Sprint(c)))
	}),
	json.MarshalToFunc(func(enc *jsontext.Encoder, p unsafe.Pointer) error {
		if p == nil {
			return enc.WriteToken(jsontext.Null)
		}
		return enc.WriteToken(jsontext.String(fmt.Sprintf("%p", p)))
	}),
)

// marshalNonFinite writes NaN and infinities as strings. For finite values it
// returns errors.ErrUnsupported without writing, so the default encoding is used.
func marshalNonFinite(enc *jsontext.Encoder, f float64) error {
	switch {
	case math.IsNaN(f):
		return enc.WriteToken(jsontext.String("NaN"))
	case math.IsInf(f, 1):
		return enc.WriteToken(jsontext.String("+Inf"))
	case math.IsInf(f, -1):
		return enc.WriteToken(jsontext.String("-Inf"))
	}
	return errors.ErrUnsupported
}

// failingDataField reports the data key named by a marshal error, and the
// Go type that could not be marshaled. It returns false when the error is not
// about a top-level field of data.
func failingDataField(err error, data map[string]any) (key, goType string, ok bool) {
	var se *json.SemanticError
	if !errors.As(err, &se) {
		return "", "", false
	}

	// Walk up from the failing value to the field directly under data.
	// LastToken returns the key unescaped.
	dataPointer := jsontext.Pointer("/context/data")
	if se.JSONPointer == dataPointer || !dataPointer.Contains(se.JSONPointer) {
		return "", "", false
	}
	field := se.JSONPointer
	for field.Parent() != dataPointer {
		field = field.Parent()
	}
	key = field.LastToken()
	if _, ok := data[key]; !ok {
		return "", "", false
	}

	goType = "unknown"
	if se.GoType != nil {
		goType = se.GoType.String()
	}
	return key, goType, true
}

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
	LineNumber   int    `json:"lineNumber,omitzero"`
	FunctionName string `json:"functionName,omitempty"`
}

type context struct {
	Data              map[string]any    `json:"data,omitempty"`
	UnsupportedFields map[string]string `json:"unsupportedFields,omitempty"`
	ReportLocation    *reportLocation   `json:"reportLocation,omitzero"`
	HTTPRequest       map[string]any    `json:"httpRequest,omitempty"`
}

type entry struct {
	Timestamp      string          `json:"timestamp,omitempty"`
	ServiceContext *serviceContext `json:"serviceContext,omitzero"`
	Message        string          `json:"message,omitempty"`
	Severity       severity        `json:"severity,omitempty"`
	Context        *context        `json:"context,omitzero"`
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

	// Copy the entry's fields because the code below deletes keys from data,
	// and the entry itself must stay unchanged for hooks and other formatters.
	data := make(map[string]any, len(e.Data))
	maps.Copy(data, e.Data)

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

	b, err := json.Marshal(ee, marshalOptions)
	for range maxUnsupportedFields {
		if err == nil {
			break
		}
		key, goType, ok := failingDataField(err, data)
		if !ok {
			break
		}
		if ee.Context.UnsupportedFields == nil {
			ee.Context.UnsupportedFields = make(map[string]string)
		}
		ee.Context.UnsupportedFields[key] = goType
		delete(data, key)
		b, err = json.Marshal(ee, marshalOptions)
	}
	if err != nil {
		return nil, fmt.Errorf("marshal log entry: %w", err)
	}

	return append(b, '\n'), nil
}
