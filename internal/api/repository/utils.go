package repository

// NullableString returns nil for "" so the INSERT/UPDATE writes a real `NULL` to nullable columns instead of an empty string. featured_image_path is the only consumer today.
func NullableString(s string) any {
	if s == "" {
		return nil
	}

	return s
}

// NullableID converts the 0-sentinel to `NULL` for the parent_id column.
func NullableID(id int64) any {
	if id == 0 {
		return nil
	}

	return id
}
