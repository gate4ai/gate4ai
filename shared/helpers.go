package shared

func PointerTo[T any](v T) *T {
	return &v
}

func StringPtrToString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

// NilIfNil returns "nil" if the string pointer is nil, otherwise returns the pointed-to string.
func NilIfNil(s *string) string {
	if s == nil {
		return "nil"
	}
	return *s
}
