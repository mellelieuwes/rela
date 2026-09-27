package memtwins_test

import (
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/twins"
	"github.com/Sourcehaven-BV/rela/internal/twins/memtwins"
	"github.com/Sourcehaven-BV/rela/internal/twins/twinstest"
)

func TestConformance(t *testing.T) {
	twinstest.RunAll(t, func(*testing.T) twins.Store {
		return memtwins.New()
	})
}
