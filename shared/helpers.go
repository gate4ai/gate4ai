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
