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

	"github.com/davecgh/go-spew/spew"
)

type StructuredError interface {
	error
	GetValues() []interface{}
	//	GetMessage() string
	WithValues(...interface{}) StructuredError
	DisableValuePrinting()
	EnableValuePrinting()
	Unwrap() error // TODO: maybe not have?
}

type structuredError struct {
	msg           string
	keysAndValues []interface{}
	print         bool
}

func keysAndValuesToSpewedMap(args ...interface{}) map[string]string {
	m := make(map[string]string)
	for i := 0; i < len(args); {
		// TODO: mostly duplicating handleFields()...?
		// there must be a better way
		key, val := args[i], args[i+1]
		if keyStr, ok := key.(string); ok {
			m[keyStr] = spew.Sdump(val)
		}
		i += 2
	}
	return m
}

// Implement `error` interface
func (e structuredError) Error() string {
	// TODO(coryrc): accept zap.Field entries?
	if e.print {
		// %v for fmt.Sprintf does print keys sorted
		return fmt.Sprintf("Error: %s\nContext:\n%v", e.msg, keysAndValuesToSpewedMap(e.keysAndValues...))
		//return fmt.Sprint(e.msg, keysAndValuesToSpewedMap(e.keysAndValues...))
	} else {
		return e.msg
	}
}

func (e structuredError) GetValues() []interface{} {
	return e.keysAndValues
}

// func (e structuredError) GetMessage() string {
// 	return e.msg
// }

func (e *structuredError) DisableValuePrinting() {
	e.print = false
}

func (e *structuredError) EnableValuePrinting() {
	e.print = true
}

func (e structuredError) Unwrap() error {
	// TODO: if error key allow unwrap? but might not always want to
	return nil
}

// Create a StructuredError. Gives a little better logging when given to a TLogger.
// TODO(coryrc): theoretical problem if we don't convert them right away and they get mutated
//   maybe save string representation right away just in case?
func Error(msg string, keysAndValues ...interface{}) *structuredError {
	return &structuredError{msg, keysAndValues, true}
}

func (e *structuredError) WithValues(keysAndValues ...interface{}) StructuredError {
	newKAV := make([]interface{}, 0, len(keysAndValues)+len(e.keysAndValues))
	newKAV = append(newKAV, e.keysAndValues...)
	newKAV = append(newKAV, keysAndValues...)
	return &structuredError{e.msg, newKAV, e.print}
}
