package preflight

import (
	"context"
	"fmt"
)

// FSWritable verifies that every listed directory exists (creating it
// with 0o755 if missing) and that the calling process can create and
// remove a file inside it. Surfaces permission, EROFS, and missing-
// parent failures without depending on any production write reaching
// them first.
type FSWritable struct {
	Dirs []string
}

func (f FSWritable) Preflight(ctx context.Context) error {
	for _, d := range f.Dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeProbe(d); err != nil {
			return fmt.Errorf("write probe %s: %w", d, err)
		}
	}
	return nil
}
