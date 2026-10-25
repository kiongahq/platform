package store

// A session is fully metered only if every reported turn supplied usage. Older
// trace producers omit this field; retain the previous state for those turns.
func mergedUsageAvailability(current *bool, metadata map[string]any) *bool {
	value, present := metadata["usage_available"].(bool)
	if !present {
		return current
	}
	if current != nil && !*current {
		return current
	}
	result := value
	return &result
}
