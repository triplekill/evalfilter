// The operations we allow users to implement use "Argument" as an
// abstract type.
//
// There are several types of arguments that we allow:
//
// * Literal integers / strings / booleans.
//
// * The result of function-calls.
//
// * The result of object/structure field-lookups.
//
// This file contains an abstract interface to define how those are
// retrieved, as well as the concrete implementations.
//

package evalfilter

import (
	"fmt"
	"os"
	"reflect"
)

// Argument is our abstract argument-type, defining the interface which
// must be implemented by any Argument-type.
//
// The operations we allow users to implement each use the abstract
// "Argument" type, which allows them to work with the various types
// of argument we allow:
//
// * Literal integers / strings / booleans.
//
// * The result of function-calls.
//
// * The result of object/structure field-lookups.
//
//
type Argument interface {

	// Value returns the value of the argument.
	//
	// The arguments here allow lookups to be made at
	// runtime - since the various implementations might
	// need access to the host runtime and the object which
	// the script is being executed against.
	Value(self *Evaluator, obj interface{}) interface{}
}

// BooleanArgument holds a literal boolean value.
type BooleanArgument struct {
	// Content holds the value.
	Content bool
}

// Value returns the boolean content we're wrapping.
func (s *BooleanArgument) Value(self *Evaluator, obj interface{}) interface{} {
	return s.Content
}

// StringArgument holds a literal string.
type StringArgument struct {
	// Content holds the string literal.
	Content string
}

// Value returns the string content we're wrapping.
func (s *StringArgument) Value(self *Evaluator, obj interface{}) interface{} {
	return s.Content
}

// FieldArgument holds a reference to an object's field value.
type FieldArgument struct {
	// Field the name of the structure/object field we return.
	Field string
}

// Value returns the value of the field from the specified object.
func (f *FieldArgument) Value(self *Evaluator, obj interface{}) interface{} {

	ref := reflect.ValueOf(obj)
	field := reflect.Indirect(ref).FieldByName(f.Field)

	switch field.Kind() {
	case reflect.Int, reflect.Int64:
		return field.Int()
	case reflect.Float32, reflect.Float64:
		return field.Float()
	case reflect.String:
		return field.String()
	case reflect.Bool:
		if field.Bool() {
			return "true"
		}
		return "false"
	}
	return nil
}

// FunctionArgument holds a reference to a function call, or
// more properly the result of calling a function.
type FunctionArgument struct {
	// Name of the function to invoke
	Function string

	// Optional arguments which will be passed to the function.
	Arguments []Argument
}

// Value returns the result of calling the function we're wrapping.
func (f *FunctionArgument) Value(self *Evaluator, obj interface{}) interface{} {

	res, ok := self.Functions[f.Function]
	if !ok {
		fmt.Printf("Unknown function: %s\n", f.Function)
		os.Exit(1)
	}

	//
	// Are we running with debugging?
	//
	if self.Debug {
		fmt.Printf("Calling function: %s\n", f.Function)
	}

	out := res.(func(eval *Evaluator, obj interface{}, args ...interface{}) interface{})

	//
	// Call the function.
	//
	ret := (out(self, obj, f.Arguments))

	//
	// Log the result?
	//
	if self.Debug {
		fmt.Printf("\tReturn: %v\n", ret)
	}

	return ret

}