package generated

// A generated helper is still summarized.
func usesGeneratedHelper() error {
	err := do()
	Helper(err) // want `error is logged by Helper`
	return err
}
