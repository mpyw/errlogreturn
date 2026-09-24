package internal

// Config is what the analyzer's flags resolve to.
type Config struct {
	// Sinks names extra functions that log every argument, spelled the way
	// types.Func.FullName prints them, with the * of a pointer receiver
	// dropped.
	Sinks map[string]bool
}
