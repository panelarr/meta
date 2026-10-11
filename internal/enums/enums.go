// SPDX-License-Identifier: GPL-3.0-or-later

// Package enums converts protobuf enum values to the short lower-case
// names meta stores in the database and accepts in filters
// (ENTRY_TYPE_LIGHT_NOVEL <-> "light_novel").
package enums

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func prefix(d protoreflect.EnumDescriptor) string {
	zero := d.Values().ByNumber(0)
	if zero == nil {
		return ""
	}
	return strings.TrimSuffix(string(zero.Name()), "UNSPECIFIED")
}

// Name returns the short name of an enum value, e.g. "light_novel".
// Unknown numbers return "unspecified".
func Name(e protoreflect.Enum) string {
	d := e.Descriptor()
	v := d.Values().ByNumber(e.Number())
	if v == nil {
		return "unspecified"
	}
	return strings.ToLower(strings.TrimPrefix(string(v.Name()), prefix(d)))
}

// Parse returns the enum value with the short name s (any case), or the
// zero value and false.
func Parse[T interface {
	~int32
	protoreflect.Enum
}](s string) (T, bool) {
	var zero T
	d := zero.Descriptor()
	v := d.Values().ByName(protoreflect.Name(prefix(d) + strings.ToUpper(strings.TrimSpace(s))))
	if v == nil {
		return zero, false
	}
	return T(v.Number()), true
}

// MustParse is Parse without the flag: unknown names give the zero value.
func MustParse[T interface {
	~int32
	protoreflect.Enum
}](s string) T {
	v, _ := Parse[T](s)
	return v
}
