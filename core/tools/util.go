package tools

import "reflect"

// typeName names a value's dynamic type for error messages.
func typeName(v any) string { return reflect.TypeOf(v).String() }
