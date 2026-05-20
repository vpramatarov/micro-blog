package repository

// NullableString returns nil for "" so the INSERT/UPDATE writes a real NULL
// to nullable columns instead of an empty string. featured_image_path is
// the only consumer today.
func NullableString(s string) any {
	if s == "" {
		return nil
	}

	return s
}
