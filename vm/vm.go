// Package vm implements a simple stack-based virtual machine.
//
// We're constructed with a set of opcodes, and we process those forever,
// or until we hit a `return` statement which terminates the program.
//
// As well as a series of opcodes to execute we're also given a set
// of constants to work with.  These are loaded to the stack on-demand,
// so they can be manipulated.
package vm

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"

	"github.com/skx/evalfilter/v2/code"
	"github.com/skx/evalfilter/v2/environment"
	"github.com/skx/evalfilter/v2/object"
	"github.com/skx/evalfilter/v2/stack"
)

// True is our global "true" object.
var True = &object.Boolean{Value: true}

// False is our global "false" object.
var False = &object.Boolean{Value: false}

// Null is our global "false" object.
var Null = &object.Null{}

// VM is the structure which holds our state.
type VM struct {

	// constants holds constants in the program source, these
	// are string-literals, numeric-literals, boolean values
	// as well as variable names, the names of functions, and
	// references to object/map values.
	//
	// constants are treated as atoms, so they are unique.
	constants []object.Object

	// bytecode contains the actual series of instructions we'll execute.
	bytecode code.Instructions

	// stack holds a pointer to our stack-object.
	//
	// We're a stack-based virtual machine so this is used for
	// much of our internal implementation.
	stack *stack.Stack

	// environment holds the environment, which will allow variables
	// and functions to be get/set.
	environment *environment.Environment

	// fields contains the contents of all the fields in the object
	// or map we're executing against.  We discover these via reflection
	// at run-time.
	//
	// Reflection is slow so the map here is used as a cache, avoiding
	// the need to reparse the same object multiple times.
	fields map[string]object.Object
}

// New constructs a new virtual machine.
func New(constants []object.Object, bytecode code.Instructions, env *environment.Environment) *VM {

	return &VM{
		constants:   constants,
		environment: env,
		bytecode:    bytecode,
		stack:       stack.New(),
	}
}

// Run launches our virtual machine, intepreting the bytecode-program we were
// constructed with.
//
// We terminate when we hit a return-operation, or if we ever hit the end of
// the supplied bytecode.  As programs can contain flow-control operation
// it is certainly possible they will never return.
//
// (Although our compiler does not implement for/while/do/until loops
// a hand-created program could build such a things via the instruction-set.)
func (vm *VM) Run(obj interface{}) (object.Object, error) {

	// Sanity-check the bytecode program is non-empty
	if len(vm.bytecode) < 1 {
		return nil, fmt.Errorf("the bytecode program is empty")
	}

	//
	// Make an empty map to store field/map contents.
	//
	vm.fields = make(map[string]object.Object)

	//
	// Instruction pointer and length.
	//
	ip := 0
	ln := len(vm.bytecode)

	//
	// Loop over all the bytecode.
	//
	// Note that the instruction set supports control-flow, so it
	// is possible we'll run forever..
	//
	for ip < ln {

		//
		// Get the next opcode
		//
		op := code.Opcode(vm.bytecode[ip])

		//
		// Find out how long it is.
		//
		opLen := code.Length(op)

		//
		// If the opcode is more than a single byte long
		// we read the argument here.
		//
		opArg := 0
		if opLen > 1 {

			//
			// Note in the future we might have to cope
			// with opcodes with more than a single argument,
			// and they might be different sizes.
			//
			opArg = int(binary.BigEndian.Uint16(vm.bytecode[ip+1 : ip+3]))
		}

		switch op {

		// NOP
		case code.OpNop:
			// NOP

			// Store an integer upon the stack
		case code.OpPush:
			vm.stack.Push(&object.Integer{Value: int64(opArg)})

			// Lookup variable/field, by name
		case code.OpConstant:

			// move the contents of a constant onto the stack
			vm.stack.Push(vm.constants[opArg])

			// Lookup variable/field, by name
		case code.OpLookup:

			// Get the name.
			name := vm.constants[opArg].Inspect()

			// Lookup the value.
			val := vm.lookup(obj, name)
			vm.stack.Push(val)

			// Set a variable by name
		case code.OpSet:

			var name object.Object
			var val object.Object
			var err error
			name, err = vm.stack.Pop()
			if err != nil {
				return nil, err
			}
			val, err = vm.stack.Pop()
			if err != nil {
				return nil, err
			}

			vm.environment.Set(name.Inspect(), val)

			// maths & comparisons
		case code.OpAdd, code.OpSub, code.OpMul, code.OpDiv, code.OpMod, code.OpPower, code.OpLess, code.OpLessEqual, code.OpGreater, code.OpGreaterEqual, code.OpEqual, code.OpNotEqual, code.OpMatches, code.OpNotMatches, code.OpAnd, code.OpOr:
			err := vm.executeBinaryOperation(op)
			if err != nil {
				return nil, err
			}

			// Store an array
		case code.OpArray:

			elements := make([]object.Object, opArg)
			for opArg > 0 {
				var err error
				elements[opArg-1], err = vm.stack.Pop()
				if err != nil {
					return nil, err
				}
				opArg--
			}
			arr := &object.Array{Elements: elements}
			vm.stack.Push(arr)

			// Lookup an array index
		case code.OpArrayIndex:
			index, err := vm.stack.Pop()
			if err != nil {
				return nil, err
			}
			left, err := vm.stack.Pop()
			if err != nil {
				return nil, err
			}

			err = vm.executeIndexExpression(left, index)
			if err != nil {
				return nil, err
			}

			// !true -> false
		case code.OpBang:

			err := vm.executeBangOperator()
			if err != nil {
				return nil, err
			}

			// -1
		case code.OpMinus:
			err := vm.executeMinusOperator()
			if err != nil {
				return nil, err
			}

			// square root
		case code.OpRoot:
			err := vm.executeSquareRoot()
			if err != nil {
				return nil, err
			}

			// Boolean literal
		case code.OpTrue:
			vm.stack.Push(True)

			// Boolean literal
		case code.OpFalse:
			vm.stack.Push(False)

			// return from script
		case code.OpReturn:
			result, err := vm.stack.Pop()
			return result, err

			// flow-control: unconditional jump
		case code.OpJump:

			// NOTE: We reduce the offset, becaues
			// at the end of our loop we increment
			// it again..

			ip = opArg - opLen

			// flow-control: jump if stack contains non-true
		case code.OpJumpIfFalse:

			condition, err := vm.stack.Pop()
			if err != nil {
				return nil, err
			}

			// If the condition evaluated to a non-true
			// then we change the IP.
			if !condition.True() {

				// NOTE: We reduce the offset, becaues
				// at the end of our loop we increment
				// it again..

				ip = opArg - opLen
			}

			// function-call: This is messy.
		case code.OpCall:

			// The OpCall instruction is followed by an
			// argument describing the number of args the
			// function we're calling should be invoked with.

			// get the name of the function from the stack.
			fName, err := vm.stack.Pop()
			if err != nil {
				return nil, err
			}

			//
			// The argument to the call-instruction is the
			// number of arguments to pass to the function
			// we're to invoke.
			//
			// Of course these are in reverse.
			//
			// Create an array and pop each stack-argument
			// off into the correct location.
			//
			fnArgs := make([]object.Object, opArg)
			for opArg > 0 {
				fnArgs[opArg-1], err = vm.stack.Pop()
				if err != nil {
					return nil, err
				}
				opArg--
			}

			// Get the function we're to invoke.
			fn, ok := vm.environment.GetFunction(fName.Inspect())
			if !ok {
				return nil, fmt.Errorf("the function %s does not exist", fName.Inspect())
			}

			// Cast the function & call it
			out := fn.(func(args []object.Object) object.Object)
			ret := out(fnArgs)

			// store the result back on the stack.
			vm.stack.Push(ret)

			// These two opcodes are just used for internal
			// use.  They are never generated, and they should
			// never be executed either.
		case code.OpCodeSingleArg, code.OpFinal:

			return nil, fmt.Errorf("tried to execute fake instruction %s - this is definitely a bug", code.String(op))

			// Can't happen?
		default:
			return nil, fmt.Errorf("unhandled opcode: %v %s", op, code.String(op))
		}

		ip += opLen
	}

	//
	// If we get here we've hit the end of the bytecode, and we
	// didn't encounter a return-instruction.
	//
	// That means the script is malformed..
	//
	// We could decide this means the script returns `false`, but
	// I'd rather users were explicit.
	//
	return nil, fmt.Errorf("missing return at the end of the script")
}

// inspectObject discovers the names/values of all structure fields, or
// map contents.
//
// This method is called the first time any reference is made to a field
// value - which means we don't eat the cost unless we need it, and we
// don't have to call reflection more than once.  (Reflection is s-l-o-w.)
func (vm *VM) inspectObject(obj interface{}) {

	//
	// If the reference is nil we have nothing to walk.
	//
	if obj == nil {
		return
	}

	//
	// Get the value, be it a "thing", or a pointer to a thing.
	//
	val := reflect.Indirect(reflect.ValueOf(obj))

	//
	// Is this a map?
	//
	if val.Kind() == reflect.Map {

		//
		// Get all keys
		//
		for _, key := range val.MapKeys() {

			// The name of the key.
			name := key.Interface().(string)

			// The actual thing inside it
			field := val.MapIndex(key).Elem()

			// Default
			var ret object.Object
			ret = &object.Null{}

			switch field.Kind() {

			// Hack.
			//
			// Probably broken.
			case reflect.Slice:
				ret = vm.createArrayFromSlice(field)
			case reflect.Int, reflect.Int64:
				ret = &object.Integer{Value: field.Int()}
			case reflect.Float32, reflect.Float64:
				ret = &object.Float{Value: field.Float()}
			case reflect.String:
				ret = &object.String{Value: field.String()}
			case reflect.Bool:
				ret = &object.Boolean{Value: field.Bool()}
			}

			vm.fields[name] = ret
		}
		return
	}

	//
	// OK this is an object
	//
	for i := 0; i < val.NumField(); i++ {

		// Get the field
		field := val.Field(i)

		// Get the name
		typeField := val.Type().Field(i)
		name := typeField.Name

		// Default
		var ret object.Object
		ret = &object.Null{}

		switch field.Kind() {

		case reflect.Slice:
			ret = vm.createArrayFromSlice(field)
		case reflect.Int, reflect.Int64:
			ret = &object.Integer{Value: field.Int()}
		case reflect.Float32, reflect.Float64:
			ret = &object.Float{Value: field.Float()}
		case reflect.String:
			ret = &object.String{Value: field.String()}
		case reflect.Bool:
			ret = &object.Boolean{Value: field.Bool()}
		}

		vm.fields[name] = ret
	}
}

// createArrayFromSlice creates an object.Array value from the
// given object/map slice.  This uses reflection and is slow/horrid
func (vm *VM) createArrayFromSlice(field reflect.Value) object.Object {

	// Elements we've found
	var el []object.Object

	// Find the length of the slice
	l := field.Len()

	// For each entry
	for i := 0; i < l; i++ {

		// Cast the array-member to an interface
		in := field.Index(i).Interface()

		//
		// Now we're in horrible-land
		//
		// We want to work out the type of the
		// array-member.  Of course every member
		// will have the same type, unless we're
		// in the case of an array of interfaces.
		//
		// The following code will try to cast
		// to all "reasonable" values, which will
		// cover either case.
		//
		// It is still horrible though, and that
		// should be noted.
		//

		// Is it a string?
		s, ok := in.(string)
		if ok {
			el = append(el, &object.String{Value: s})
			continue
		}

		// Is it a bool?
		b, ok := in.(bool)
		if ok {
			el = append(el, &object.Boolean{Value: b})
			continue
		}

		// is it a float?
		f, ok := in.(float32)
		if ok {
			el = append(el, &object.Float{Value: float64(f)})
			continue
		}
		ff, ok := in.(float64)
		if ok {
			el = append(el, &object.Float{Value: ff})
			continue
		}

		// is it an integer?
		d, ok := in.(int)
		if ok {
			el = append(el, &object.Integer{Value: int64(d)})
			continue
		}
		dd, ok := in.(int32)
		if ok {
			el = append(el, &object.Integer{Value: int64(dd)})
			continue
		}
		ddd, ok := in.(int64)
		if ok {
			el = append(el, &object.Integer{Value: ddd})
			continue
		}

		// FATAL!
		fmt.Printf("Failed to convert array-member to object")
		os.Exit(1)
	}

	return &object.Array{Elements: el}
}

// Execute an operation against two arguments, i.e "foo == bar", "2 + 3", etc.
//
// This is a crazy-big function, because we have to cope with different operand
// types and operators.
func (vm *VM) executeBinaryOperation(op code.Opcode) error {
	var left object.Object
	var right object.Object
	var err error

	right, err = vm.stack.Pop()
	if err != nil {
		return err
	}
	left, err = vm.stack.Pop()
	if err != nil {
		return err
	}

	switch {
	case left.Type() == object.INTEGER && right.Type() == object.INTEGER:
		return vm.evalIntegerInfixExpression(op, left, right)
	case left.Type() == object.FLOAT && right.Type() == object.FLOAT:
		return vm.evalFloatInfixExpression(op, left, right)
	case left.Type() == object.FLOAT && right.Type() == object.INTEGER:
		return vm.evalFloatIntegerInfixExpression(op, left, right)
	case left.Type() == object.INTEGER && right.Type() == object.FLOAT:
		return vm.evalIntegerFloatInfixExpression(op, left, right)
	case left.Type() == object.STRING && right.Type() == object.STRING:
		return vm.evalStringInfixExpression(op, left, right)
	case op == code.OpAnd:
		// if left is false skip right
		if !left.True() {
			vm.stack.Push(False)
			return nil
		}
		if right.True() {
			vm.stack.Push(True)
		} else {
			vm.stack.Push(False)
		}
		return nil
	case op == code.OpOr:
		// if left is true skip right
		if left.True() {
			vm.stack.Push(True)
			return nil
		}
		if right.True() {
			vm.stack.Push(True)
		} else {
			vm.stack.Push(False)
		}
		return nil
	case left.Type() == object.BOOLEAN && right.Type() == object.BOOLEAN:
		return vm.evalBooleanInfixExpression(op, left, right)
	case left.Type() != right.Type():
		return fmt.Errorf("type mismatch: %s %s %s",
			left.Type(), code.String(op), right.Type())
	default:
		return fmt.Errorf("unknown operator: %s %s %s",
			left.Type(), code.String(op), right.Type())
	}
}

// integer OP integer
func (vm *VM) evalIntegerInfixExpression(op code.Opcode, left, right object.Object) error {
	leftVal := left.(*object.Integer).Value
	rightVal := right.(*object.Integer).Value

	switch op {
	case code.OpAdd:
		vm.stack.Push(&object.Integer{Value: leftVal + rightVal})
	case code.OpSub:
		vm.stack.Push(&object.Integer{Value: leftVal - rightVal})
	case code.OpMul:
		vm.stack.Push(&object.Integer{Value: leftVal * rightVal})
	case code.OpDiv:
		if rightVal == 0 {
			return fmt.Errorf("attempted division by zero: %d / %d", leftVal, rightVal)
		}
		vm.stack.Push(&object.Integer{Value: leftVal / rightVal})
	case code.OpMod:
		vm.stack.Push(&object.Integer{Value: leftVal % rightVal})
	case code.OpPower:
		vm.stack.Push(&object.Integer{Value: int64(math.Pow(float64(leftVal), float64(rightVal)))})
	case code.OpLess:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal < rightVal))
	case code.OpLessEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal <= rightVal))
	case code.OpGreater:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal > rightVal))
	case code.OpGreaterEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal >= rightVal))
	case code.OpEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal == rightVal))
	case code.OpNotEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal != rightVal))
	default:
		return (fmt.Errorf("unknown operator: %s %s %s", left.Type(), code.String(op), right.Type()))
	}

	return nil
}

// float OP float
func (vm *VM) evalFloatInfixExpression(op code.Opcode, left, right object.Object) error {
	leftVal := left.(*object.Float).Value
	rightVal := right.(*object.Float).Value

	switch op {
	case code.OpAdd:
		vm.stack.Push(&object.Float{Value: leftVal + rightVal})
	case code.OpSub:
		vm.stack.Push(&object.Float{Value: leftVal - rightVal})
	case code.OpMul:
		vm.stack.Push(&object.Float{Value: leftVal * rightVal})
	case code.OpDiv:
		if rightVal == 0 {
			return fmt.Errorf("attempted division by zero: %f / %f", leftVal, rightVal)
		}
		vm.stack.Push(&object.Float{Value: leftVal / rightVal})
	case code.OpMod:
		vm.stack.Push(&object.Float{Value: float64(int(leftVal) % int(rightVal))})
	case code.OpPower:
		vm.stack.Push(&object.Float{Value: math.Pow(leftVal, rightVal)})
	case code.OpLess:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal < rightVal))
	case code.OpLessEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal <= rightVal))
	case code.OpGreater:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal > rightVal))
	case code.OpGreaterEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal >= rightVal))
	case code.OpEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal == rightVal))
	case code.OpNotEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal != rightVal))
	default:
		return (fmt.Errorf("unknown operator: %s %s %s", left.Type(), code.String(op), right.Type()))
	}

	return nil
}

// float OP int
func (vm *VM) evalFloatIntegerInfixExpression(op code.Opcode, left, right object.Object) error {
	leftVal := left.(*object.Float).Value
	rightVal := float64(right.(*object.Integer).Value)

	switch op {
	case code.OpAdd:
		vm.stack.Push(&object.Float{Value: leftVal + rightVal})
	case code.OpSub:
		vm.stack.Push(&object.Float{Value: leftVal - rightVal})
	case code.OpMul:
		vm.stack.Push(&object.Float{Value: leftVal * rightVal})
	case code.OpDiv:
		if rightVal == 0 {
			return fmt.Errorf("attempted division by zero: %f / %f", leftVal, rightVal)
		}
		vm.stack.Push(&object.Float{Value: leftVal / rightVal})
	case code.OpMod:
		vm.stack.Push(&object.Float{Value: float64(int(leftVal) % int(rightVal))})
	case code.OpPower:
		vm.stack.Push(&object.Float{Value: math.Pow(leftVal, rightVal)})
	case code.OpLess:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal < rightVal))
	case code.OpLessEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal <= rightVal))
	case code.OpGreater:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal > rightVal))
	case code.OpGreaterEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal >= rightVal))
	case code.OpEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal == rightVal))
	case code.OpNotEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal != rightVal))
	default:
		return (fmt.Errorf("unknown operator: %s %s %s", left.Type(), code.String(op), right.Type()))
	}

	return nil
}

// int OP float
func (vm *VM) evalIntegerFloatInfixExpression(op code.Opcode, left, right object.Object) error {
	leftVal := float64(left.(*object.Integer).Value)
	rightVal := right.(*object.Float).Value

	switch op {
	case code.OpAdd:
		vm.stack.Push(&object.Float{Value: leftVal + rightVal})
	case code.OpSub:
		vm.stack.Push(&object.Float{Value: leftVal - rightVal})
	case code.OpMul:
		vm.stack.Push(&object.Float{Value: leftVal * rightVal})
	case code.OpDiv:
		if rightVal == 0 {
			return fmt.Errorf("attempted division by zero: %f / %f", leftVal, rightVal)
		}
		vm.stack.Push(&object.Float{Value: leftVal / rightVal})
	case code.OpMod:
		vm.stack.Push(&object.Float{Value: float64(int(leftVal) % int(rightVal))})
	case code.OpPower:
		vm.stack.Push(&object.Float{Value: math.Pow(leftVal, rightVal)})
	case code.OpLess:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal < rightVal))
	case code.OpLessEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal <= rightVal))
	case code.OpGreater:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal > rightVal))
	case code.OpGreaterEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal >= rightVal))
	case code.OpEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal == rightVal))
	case code.OpNotEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(leftVal != rightVal))
	default:
		return (fmt.Errorf("unknown operator: %s %s %s", left.Type(), code.String(op), right.Type()))
	}

	return nil
}

// string OP string
func (vm *VM) evalStringInfixExpression(op code.Opcode, left object.Object, right object.Object) error {
	l := left.(*object.String)
	r := right.(*object.String)

	switch op {
	case code.OpEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(l.Value == r.Value))
	case code.OpNotEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(l.Value != r.Value))
	case code.OpGreaterEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(l.Value >= r.Value))
	case code.OpGreater:
		vm.stack.Push(vm.nativeBoolToBooleanObject(l.Value > r.Value))
	case code.OpLessEqual:
		vm.stack.Push(vm.nativeBoolToBooleanObject(l.Value <= r.Value))
	case code.OpLess:
		vm.stack.Push(vm.nativeBoolToBooleanObject(l.Value < r.Value))
	case code.OpMatches:
		args := []object.Object{l, r}
		fn, ok := vm.environment.GetFunction("match")
		if !ok {
			return (fmt.Errorf("failed to lookup match-function"))
		}
		out := fn.(func(args []object.Object) object.Object)
		ret := out(args)

		if ret.(*object.Boolean).Value {
			vm.stack.Push(True)
		} else {
			vm.stack.Push(False)
		}
	case code.OpNotMatches:
		args := []object.Object{l, r}
		fn, ok := vm.environment.GetFunction("match")
		if !ok {
			return (fmt.Errorf("failed to lookup match-function"))
		}
		out := fn.(func(args []object.Object) object.Object)
		ret := out(args)

		if ret.(*object.Boolean).Value {
			vm.stack.Push(False)
		} else {
			vm.stack.Push(True)
		}

	case code.OpAdd:
		vm.stack.Push(&object.String{Value: l.Value + r.Value})
	default:
		return (fmt.Errorf("unknown operator: %s %s %s", left.Type(), code.String(op), right.Type()))
	}

	return nil
}

// bool OP bool
func (vm *VM) evalBooleanInfixExpression(op code.Opcode, left object.Object, right object.Object) error {
	// convert the bools to strings.
	l := &object.String{Value: left.Inspect()}
	r := &object.String{Value: right.Inspect()}

	// then reuse our implementation, which will work
	// but might give some "interesting" results.
	//
	// e.g. "false < true"
	//
	return (vm.evalStringInfixExpression(op, l, r))
}

// Implement the "!" (prefix) operator.
func (vm *VM) executeBangOperator() error {
	operand, err := vm.stack.Pop()
	if err != nil {
		return err
	}

	switch operand {
	case True:
		vm.stack.Push(False)
	case False:
		vm.stack.Push(True)
	case Null:
		vm.stack.Push(True)
	default:
		vm.stack.Push(False)
	}
	return nil
}

// Allow negative numbers.
func (vm *VM) executeMinusOperator() error {
	operand, err := vm.stack.Pop()
	if err != nil {
		return err
	}
	var res object.Object

	switch obj := operand.(type) {
	case *object.Integer:
		res = &object.Integer{Value: -obj.Value}
	case *object.Float:
		res = &object.Float{Value: -obj.Value}
	default:
		return fmt.Errorf("unsupported type for negation: %s", operand.Type())
	}

	vm.stack.Push(res)
	return nil
}

// The square root operation is just too cute :).
func (vm *VM) executeSquareRoot() error {
	operand, err := vm.stack.Pop()
	if err != nil {
		return err
	}
	var res object.Object

	switch obj := operand.(type) {
	case *object.Integer:
		res = &object.Float{Value: math.Sqrt(float64(obj.Value))}
	case *object.Float:
		res = &object.Float{Value: math.Sqrt(obj.Value)}
	default:
		return fmt.Errorf("unsupported type for square-root: %s", operand.Type())
	}

	vm.stack.Push(res)
	return nil
}

// convert a native (go) boolean to an Object
func (vm *VM) nativeBoolToBooleanObject(input bool) *object.Boolean {
	if input {
		return True
	}
	return False
}

// lookup the name of the given field/map-member.
func (vm *VM) lookup(obj interface{}, name string) object.Object {

	//
	// Remove legacy "$" prefix, if present.
	//
	name = strings.TrimPrefix(name, "$")

	//
	// Look for this as a variable first, they take precedence.
	//
	if val, ok := vm.environment.Get(name); ok {
		return val
	}

	//
	// Now we assume this is a reference to a map-key, or
	// object member.
	//
	// If we've not discovered them then do so now
	//
	if len(vm.fields) == 0 {
		vm.inspectObject(obj)
	}

	//
	// Now perform the lookup
	//
	if cached, found := vm.fields[name]; found {
		return cached
	}

	//
	// If it was not found it is an unknown/unset value.
	//
	return Null
}

// executeIndexExpression lookup the array value at the given index.
func (vm *VM) executeIndexExpression(left, index object.Object) error {

	// Check arguments
	if left.Type() != object.ARRAY && left.Type() != object.STRING {
		return fmt.Errorf("the index operator can only be applied to strings and arrays, not %s", left.Type())
	}
	if index.Type() != object.INTEGER {
		return fmt.Errorf("index operator must be given an integer, not %s", index.Type())
	}

	// Get the index we should lookup
	idx := index.(*object.Integer).Value

	// Looking at a string?
	if left.Type() == object.STRING {

		str := left.(*object.String).Inspect()
		if idx < 0 || int(idx) > len(str) {
			vm.stack.Push(Null)
			return nil
		}
		vm.stack.Push(&object.String{Value: string(str[idx])})
		return nil
	}

	// OK here we know we're dealing with an array.
	arrayObject := left.(*object.Array)

	// bounds-check
	max := int64(len(arrayObject.Elements) - 1)
	if idx < 0 || idx > max {
		vm.stack.Push(Null)
		return nil
	}

	// Return the appropriate object.
	vm.stack.Push(arrayObject.Elements[idx])
	return nil
}
