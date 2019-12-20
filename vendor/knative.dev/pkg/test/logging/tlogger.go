/*
Copyright 2019 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logging

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
)

//  1. Structured versions of t.Error() and t.Fatal()
//  2. A replacement t.Run() for subtests, which calls a subfunction func(t *TLogger) instead
//  3. Implement test.T and test.TLegacy for compat reasons

type TLogger struct {
	l     *zap.Logger
	level int
	t     *testing.T
	e     map[string][]interface{}
}

func (o *TLogger) V(level int) logr.InfoLogger {
	// Consider adding || (level <= logrZapDebugLevel && o.l.Core().Enabled(zapLevelFromLogrLevel(level)))
	// Reason to add it is even if you ask for verbosity=1, in case of error you'll get up to verbosity=3 in the debug output
	// but since zapTest uses Debug, you always get V(<=3) even when verbosity < 3
	// Probable solution is to write to t.Log at Info level?
	if level <= o.level {
		return &infoLogger{
			logrLevel: o.level,
			t:         o,
		}
	}
	return disabledInfoLogger
}

func (o *TLogger) WithValues(keysAndValues ...interface{}) *TLogger {
	return o.cloneWithNewLogger(o.l.With(o.handleFields(keysAndValues)...))
}

func (o *TLogger) WithName(name string) *TLogger {
	return o.cloneWithNewLogger(o.l.Named(name))
}

// Custom additions:

//
// Intended to be similar to go-logr's Error() method
func (o *TLogger) ErrorIfErr(err error, msg string, keysAndValues ...interface{}) {
	if err != nil {
		o.error(err, msg, keysAndValues)
		o.t.Fail()
	}
}

//
// Intended to be similar to go-logr's Error() method
func (o *TLogger) FatalIfErr(err error, msg string, keysAndValues ...interface{}) {
	if err != nil {
		o.error(err, msg, keysAndValues)
		o.t.FailNow()
	}
}

// Intended usage is Error(msg string, key-value alternating arguments)
// Same effect as testing.T.Error
// Generic definition for compatibility with test.T interface
func (o *TLogger) Error(keysAndValues ...interface{}) {
	// Using o.error to have consistent call depth for Error, FatalIfErr, Info, etc
	o.error(o.errorWithRuntimeCheck(keysAndValues...))
	o.t.Fail()
}

// Intended usage is Fatal(msg string, key-value alternating arguments)
// Same effect as testing.T.Fatal
// Generic definition for compatibility with test.TLegacy interface
func (o *TLogger) Fatal(keysAndValues ...interface{}) {
	o.error(o.errorWithRuntimeCheck(keysAndValues...))
	o.t.FailNow()
}

func (o *TLogger) errorWithRuntimeCheck(keysAndValues ...interface{}) (error, string, []interface{}) {
	if len(keysAndValues) == 0 {
		return nil, "", nil
	} else {
		s, isString := keysAndValues[0].(string)
		e, isError := keysAndValues[0].(error)
		if isString {
			// Desired case (probably)
			return nil, s, keysAndValues[1:]
		} else if isError && len(keysAndValues) == 1 {
			return e, "", nil
		} else {
			// Treat as untrustworthy data
			if o.V(8).Enabled() {
				o.l.Sugar().Debugw("DEPRECATED Error/Fatal usage", zap.Stack("callstack"))
			}
			fields := make([]interface{}, 2*len(keysAndValues))
			for i, d := range keysAndValues {
				if i == 0 && isError {
					fields[0] = "error"
					fields[1] = d
				} else {
					fields[i*2] = fmt.Sprintf("arg %d", i)
					fields[i*2+1] = d
				}
			}
			return nil, "unstructured error", fields
		}
	}
}

func (o *TLogger) Run(name string, f func(t *TLogger)) {
	tfunc := func(ts *testing.T) {
		tl := newTLogger(ts, o.level)
		f(tl)
		tl.handleCollectedErrors()
	}
	o.t.Run(name, tfunc)
}

// Interface test.T

// Just like testing.T.Name()
func (o *TLogger) Name() string {
	return o.t.Name()
}

// T.Helper() cannot work as an indirect call, so just do nothing
func (o *TLogger) Helper() {
}

// Just like testing.T.SkipNow()
func (o *TLogger) SkipNow() {
	o.t.SkipNow()
}

// Deprecated: only existing for test.T compatibility
// Will panic if given data incompatible with Info() function
func (o *TLogger) Log(args ...interface{}) {
	// This is complicated to ensure exactly 2 levels of indirection
	i := o.V(2)
	iL, ok := i.(*infoLogger)
	if ok {
		iL.indirectWrite(args[0].(string), args[1:]...)
	}
}

// Just like testing.T.Parallel()
func (o *TLogger) Parallel() {
	o.t.Parallel()
}

// Interface test.TLegacy
// Fatal() is an intended function

// Deprecated. Just like testing.T.Logf()
func (o *TLogger) Logf(fmtS string, args ...interface{}) {
	// This is complicated to ensure exactly 2 levels of indirection
	iL, ok := o.V(2).(*infoLogger)
	if ok {
		iL.indirectWrite(fmt.Sprintf(fmtS, args...))
	}
}

func (o *TLogger) error(err error, msg string, keysAndValues []interface{}) {
	var newKAV []interface{}
	/*
		var serr StructuredError
		if errors.As(err, &serr) {
			serr.DisableValuePrinting()
			defer serr.EnableValuePrinting()
			newLen := len(keysAndValues) + len(serr.GetValues())
			newKAV = make([]interface{}, 0, newLen+2)
			newKAV = append(newKAV, keysAndValues...)
			newKAV = append(newKAV, serr.GetValues()...)
		}*/
	if err != nil {
		if msg == "" { // This is used if just the error is given to .Error() or .Fatal()
			msg = err.Error()
		} else {
			if newKAV == nil {
				newKAV = make([]interface{}, 0, len(keysAndValues)+1)
				newKAV = append(newKAV, keysAndValues...)
			}
			newKAV = append(newKAV, zap.Error(err))
		}
	}
	if newKAV != nil {
		keysAndValues = newKAV
	}
	if checkedEntry := o.l.Check(zap.ErrorLevel, msg); checkedEntry != nil {
		checkedEntry.Write(o.handleFields(keysAndValues)...)
	}
}

// Creation and Teardown

// Create a TLogger object using the global Zap logger and the current testing.T
func NewTLogger(t *testing.T) *TLogger {
	return newTLogger(t, Verbosity)
}

func newTLogger(t *testing.T, verbosity int) *TLogger {
	testOptions := []zap.Option{
		zap.AddCaller(),
		zap.AddCallerSkip(2),
		zap.Development(),
	}
	core := zaptest.NewLogger(t).Core()
	if zapCore != nil {
		core = zapcore.NewTee(
			zapCore,
			core,
			// TODO(coryrc): Open new file (maybe creating JUnit!?) with test output?
		)
	}
	log := zap.New(core).Named(t.Name()).WithOptions(testOptions...)
	tlogger := TLogger{
		l:     log,
		level: verbosity,
		t:     t,
		e:     make(map[string][]interface{}, 0),
	}
	return &tlogger
}

func (o *TLogger) cloneWithNewLogger(l *zap.Logger) *TLogger {
	t := TLogger{
		l:     l,
		level: o.level,
		t:     o.t,
		e:     o.e,
	}
	return &t
}

func (o *TLogger) Collect(key string, value interface{}) {
	list, has_key := o.e[key]
	if has_key {
		list = append(list, value)
	} else {
		list = make([]interface{}, 1)
		list[0] = value
	}
	o.e[key] = list
}

func (o *TLogger) handleCollectedErrors() {
	for name, list := range o.e {
		o.Run(name, func(t *TLogger) {
			for _, item := range list {
				_, isError := item.(error)
				if isError {
					t.Error(item)
				} else {
					t.V(3).Info(spewConfig.Sprint(item))
				}
			}
		})
	}
}

// Please `defer t.CleanUp()` after invoking NewTLogger()
func (o *TLogger) CleanUp() {
	o.handleCollectedErrors()

	// Ensure nothing can log to t after test is complete
	// TODO(coryrc): except .WithName(), etc create a new logger
	//   can we somehow overwrite the core?
	//   or change the core's LevelEnabler so it can't fire!
	o.l = logger
	o.t = nil
}
