package editorial_test

import (
	"testing"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/internal/store/storetest"
)

// The same contract runs against PostgreSQL in go/integration.
func TestEditorialConformanceOnFileStore(t *testing.T) {
	storetest.Editorial(t, store.New())
}
