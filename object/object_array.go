package object

import (
	"bytes"
	"strings"
)

// Array wraps Object array and implements Object interface.
type Array struct {
	// Elements holds the individual members of the array we're wrapping.
	Elements []Object
}

// Type returns the type of this object.
func (ao *Array) Type() Type {
	return ARRAY
}

// Inspect returns a string-representation of the given object.
func (ao *Array) Inspect() string {
	var out bytes.Buffer
	elements := make([]string, 0)
	for _, e := range ao.Elements {
		elements = append(elements, e.Inspect())
	}
	out.WriteString("[")
	out.WriteString(strings.Join(elements, ", "))
	out.WriteString("]")
	return out.String()
}

// True returns whether this object wraps a true-like value.
//
// Used when this object is the conditional in a comparison, etc.
func (ao *Array) True() bool {
	return (len(ao.Elements) != 0)
}
