package evidence

import (
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

var ErrBudget = errors.New("JSON validation budget exceeded")
var ErrUnavailable = errors.New("JSON validation unavailable")
var ErrInvalid = errors.New("invalid JSON value")

// CopyJSON owns a bounded compatibility JSON clone, without application serializers.
// Plain Go structs retain the encoding/json field/tag semantics used by installed hooks.
func CopyJSON(value any) (owned map[string]any, failure error) {
	defer func() {
		if recover() != nil {
			owned = nil
			failure = ErrUnavailable
		}
	}()
	if err := checkNative(value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(encoded) > 8*1024*1024 {
		return nil, ErrBudget
	}
	if json.Unmarshal(encoded, &owned) != nil || owned == nil {
		return nil, ErrInvalid
	}
	if err := checkNative(owned); err != nil {
		return nil, err
	}
	return owned, nil
}
func checkNative(input any) error {
	nodes, bytes := 4096, 8*1024*1024
	var visit func(reflect.Value, int) error
	visit = func(value reflect.Value, depth int) error {
		nodes--
		bytes -= 8
		if nodes < 0 || bytes < 0 || depth > 16 {
			return ErrBudget
		}
		if !value.IsValid() {
			return nil
		}
		if value.CanInterface() {
			switch item := value.Interface().(type) {
			case time.Time:
				bytes -= 64
				return nil
			case json.RawMessage:
				bytes -= len(item)
				if bytes < 0 {
					return ErrBudget
				}
				return nil
			case json.Marshaler, encoding.TextMarshaler:
				return ErrUnavailable
			}
		}
		switch value.Kind() {
		case reflect.Interface, reflect.Pointer:
			if !value.IsNil() {
				return visit(value.Elem(), depth)
			}
		case reflect.String:
			bytes -= value.Len()
		case reflect.Map:
			if value.Type().Key().Kind() != reflect.String {
				return ErrInvalid
			}
			if value.Len() > nodes/2 {
				return ErrBudget
			}
			iterator := value.MapRange()
			for iterator.Next() {
				if err := visit(iterator.Key(), depth+1); err != nil {
					return err
				}
				if err := visit(iterator.Value(), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if value.Len() > nodes {
				return ErrBudget
			}
			for index := 0; index < value.Len(); index++ {
				if err := visit(value.Index(index), depth+1); err != nil {
					return err
				}
			}
		case reflect.Struct:
			if value.NumField() > 256 {
				return ErrBudget
			}
			for index := 0; index < value.NumField(); index++ {
				field := value.Type().Field(index)
				if field.PkgPath != "" || field.Tag.Get("json") == "-" {
					continue
				}
				bytes -= len(field.Name) + len(field.Tag.Get("json"))
				if err := visit(value.Field(index), depth+1); err != nil {
					return err
				}
			}
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		default:
			return ErrInvalid
		}
		if bytes < 0 {
			return ErrBudget
		}
		return nil
	}
	return visit(reflect.ValueOf(input), 0)
}
