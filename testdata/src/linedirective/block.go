package linedirective

import "log"

// The block form moves only the call to line 50. The ignore above it is on
// the line above in the file.
func block() error {
	err := do()
	//errlogreturn:ignore
	/*line block.go:50*/ log.Println(err)
	return err
}
