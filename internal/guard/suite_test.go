package guard_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGuard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Guard Suite")
}
