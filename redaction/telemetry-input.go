package redaction

import (
	"encoding"
	"encoding/json"
	"reflect"
	"time"
)

// Bound the JSON compatibility clone before encoding. Encoding first would allocate
// arbitrary caller-sized buffers and execute application MarshalJSON callbacks.
func checkTelemetryInput(input any) error {
	nodes, bytes := 0, 0
	var visit func(reflect.Value, int) error
	visit = func(value reflect.Value, depth int) error {
		nodes++
		bytes += 4
		if nodes > 65536 || bytes > maxTelemetryBytes || depth > 64 {
			return ErrTelemetryBudget
		}
		if !value.IsValid() {
			return nil
		}
		if value.CanInterface() {
			switch current := value.Interface().(type) {
			case time.Time:
				bytes += 64
				return nil
			case json.RawMessage:
				bytes += len(current)
				if bytes > maxTelemetryBytes {
					return ErrTelemetryBudget
				}
				return nil
			case json.Marshaler, encoding.TextMarshaler:
				return ErrUnsafeTelemetry
			}
		}
		switch value.Kind() {
		case reflect.Interface, reflect.Pointer:
			if !value.IsNil() {
				return visit(value.Elem(), depth+1)
			}
		case reflect.String:
			bytes += value.Len()
		case reflect.Map:
			if value.Len() > 65536 || value.Type().Key().Kind() != reflect.String {
				return ErrUnsafeTelemetry
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
			if value.Len() > 65536 {
				return ErrTelemetryBudget
			}
			for index := 0; index < value.Len(); index++ {
				if err := visit(value.Index(index), depth+1); err != nil {
					return err
				}
			}
		case reflect.Struct:
			if value.NumField() > 256 {
				return ErrTelemetryBudget
			}
			for index := 0; index < value.NumField(); index++ {
				field := value.Type().Field(index)
				if field.PkgPath != "" || field.Tag.Get("json") == "-" {
					continue
				}
				bytes += len(field.Name) + len(field.Tag.Get("json"))
				if err := visit(value.Field(index), depth+1); err != nil {
					return err
				}
			}
		}
		if bytes > maxTelemetryBytes {
			return ErrTelemetryBudget
		}
		return nil
	}
	return visit(reflect.ValueOf(input), 0)
}
