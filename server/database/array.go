package database

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
)

// Array stores a Go slice in a SQLite TEXT column as a JSON array. It is a drop-in
// replacement for pq.Array: pass the slice (or a pointer to it) as a query argument, or a
// pointer to the slice as a Scan destination.
func Array(slice any) jsonArray {
	return jsonArray{slice}
}

type jsonArray struct {
	slice any
}

// Value implements driver.Valuer.
func (a jsonArray) Value() (driver.Value, error) {
	rv := reflect.ValueOf(a.slice)
	for rv.IsValid() && (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return "[]", nil
		}
		rv = rv.Elem()
	}

	if !rv.IsValid() || (rv.Kind() == reflect.Slice && rv.IsNil()) {
		return "[]", nil
	}

	b, err := json.Marshal(rv.Interface())
	if err != nil {
		return nil, err
	}

	return string(b), nil
}

// Scan implements sql.Scanner. The destination given to Array must be a pointer to a slice.
func (a jsonArray) Scan(src any) error {
	dest := reflect.ValueOf(a.slice)
	if dest.Kind() != reflect.Ptr || dest.IsNil() {
		return fmt.Errorf("database.Array: Scan destination must be a non-nil pointer to a slice, got %T", a.slice)
	}

	var data []byte
	switch s := src.(type) {
	case nil:
	case string:
		data = []byte(s)
	case []byte:
		data = s
	default:
		return fmt.Errorf("database.Array: cannot scan %T", src)
	}

	if len(data) == 0 {
		dest.Elem().Set(reflect.Zero(dest.Elem().Type()))
		return nil
	}

	return json.Unmarshal(data, a.slice)
}
