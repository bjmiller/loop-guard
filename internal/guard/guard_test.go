package guard_test

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/brian/loop-guard/internal/guard"
	"github.com/brian/loop-guard/internal/state"
)

func toolEvent(name string, args map[string]any) state.Event {
	return state.Event{Type: "tool", Name: name, Args: args}
}

var _ = Describe("Guard", func() {
	var dir string
	var clock *state.FakeClock
	var g *guard.Guard

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		clock = state.NewFakeClock(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
		g = &guard.Guard{Store: &state.Store{Dir: dir, Clock: clock}, MaxInterventions: 2}
	})

	Describe("recording non-looping traffic", func() {
		It("allows varied tool calls", func() {
			v, err := g.Record("s1", toolEvent("bash", map[string]any{"command": "ls"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(v.OK).To(BeTrue())
			Expect(v.Loop).To(BeFalse())
			Expect(v.Action).To(Equal(guard.ActionAllow))
		})

		It("does not flag two identical calls", func() {
			for i := 0; i < 2; i++ {
				v, err := g.Record("s1", toolEvent("bash", map[string]any{"command": "ls"}))
				Expect(err).NotTo(HaveOccurred())
				Expect(v.Loop).To(BeFalse())
			}
		})
	})

	Describe("escalation ladder", func() {
		sameCall := func() state.Event {
			return toolEvent("bash", map[string]any{"command": "flaky-test --retry"})
		}

		It("injects a recovery prompt on first detection", func() {
			v, err := g.Record("s1", sameCall())
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Action).To(Equal(guard.ActionAllow))

			v, err = g.Record("s1", sameCall())
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Action).To(Equal(guard.ActionAllow))

			v, err = g.Record("s1", sameCall()) // 3rd identical call trips detection
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Loop).To(BeTrue())
			Expect(v.Action).To(Equal(guard.ActionInject))
			Expect(v.Kind).To(Equal("tool"))
			Expect(v.Interventions).To(Equal(1))
			Expect(v.Message).To(ContainSubstring("loop"))
		})

		It("escapes args key order when matching fingerprints", func() {
			_, _ = g.Record("s2", toolEvent("write", map[string]any{"path": "/f", "content": "x"}))
			_, _ = g.Record("s2", toolEvent("write", map[string]any{"path": "/f", "content": "x"}))
			v, err := g.Record("s2", toolEvent("write", map[string]any{"content": "x", "path": "/f"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Loop).To(BeTrue())
		})

		It("issues a final warning on second detection", func() {
			for i := 0; i < 3; i++ {
				_, err := g.Record("s3", sameCall())
				Expect(err).NotTo(HaveOccurred())
			}
			v, err := g.Record("s3", sameCall()) // 4th identical call → intervention 2
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Action).To(Equal(guard.ActionInjectFinal))
			Expect(v.Interventions).To(Equal(2))
			Expect(strings.ToLower(v.Message)).To(ContainSubstring("final"))
		})

		It("trips the breaker on third detection and writes a handoff artifact", func() {
			for i := 0; i < 4; i++ {
				_, err := g.Record("s4", sameCall())
				Expect(err).NotTo(HaveOccurred())
			}
			v, err := g.Record("s4", sameCall()) // 5th identical call → breaker
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Action).To(Equal(guard.ActionBreaker))
			Expect(v.HandoffPath).NotTo(BeEmpty())

			data, err := os.ReadFile(v.HandoffPath)
			Expect(err).NotTo(HaveOccurred())
			h := string(data)
			Expect(h).To(ContainSubstring("Loop-Guard Handoff"))
			Expect(h).To(ContainSubstring("Observed loop"))
			Expect(h).To(ContainSubstring("Recent activity"))
			Expect(h).To(ContainSubstring("Interventions attempted"))
			Expect(h).To(ContainSubstring("bash"))

			sess, loadErr := (&state.Store{Dir: dir, Clock: clock}).Load("s4")
			Expect(loadErr).NotTo(HaveOccurred())
			Expect(sess.Breaker).To(BeTrue())
			Expect(sess.Interventions).To(Equal(3))
		})

		It("short-circuits everything once the breaker is tripped", func() {
			for i := 0; i < 5; i++ {
				_, _ = g.Record("s5", sameCall())
			}
			v, err := g.Record("s5", toolEvent("bash", map[string]any{"command": "different command"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Action).To(Equal(guard.ActionBreaker))
		})
	})

	Describe("response loops", func() {
		longMsg := "The build failed because the test suite timed out after thirty seconds."

		It("detects near-duplicate responses and escalates", func() {
			var v guard.Verdict
			texts := []string{
				longMsg,
				"The build FAILED because the test suite TIMED OUT after thirty seconds!",
				"the build failed because the test suite timed out after thirty seconds",
			}
			for _, t := range texts {
				v, _ = g.Record("s6", state.Event{Type: "response", Text: t})
			}
			Expect(v.Loop).To(BeTrue())
			Expect(v.Kind).To(Equal("response"))
			Expect(v.Action).To(Equal(guard.ActionInject))
		})
	})

	Describe("Check", func() {
		It("evaluates without mutating state", func() {
			for i := 0; i < 4; i++ { // interventions reach 2, breaker not yet tripped
				_, err := g.Record("s7", toolEvent("bash", map[string]any{"command": "ls"}))
				Expect(err).NotTo(HaveOccurred())
			}
			store := &state.Store{Dir: dir, Clock: clock}
			before, _ := store.Load("s7")
			Expect(before.Interventions).To(Equal(2))

			v := g.Check("s7")
			Expect(v.Loop).To(BeTrue())
			Expect(v.Action).To(Equal(guard.ActionBreaker)) // next occurrence would trip it

			after, _ := store.Load("s7")
			Expect(after.Interventions).To(Equal(before.Interventions))
			Expect(after.Breaker).To(BeFalse())
		})

		It("returns an allow verdict for empty sessions", func() {
			v := g.Check("nope")
			Expect(v.Action).To(Equal(guard.ActionAllow))
		})
	})
})

var _ = Describe("RecoveryMessage", func() {
	It("differs between first and final warnings", func() {
		first := guard.RecoveryMessage("tool", 3, false)
		final := guard.RecoveryMessage("tool", 4, true)
		Expect(first).NotTo(Equal(final))
		Expect(strings.ToLower(final)).To(ContainSubstring("final"))
	})
})

var _ = Describe("HandoffPathFor", func() {
	It("lives beside the session state", func() {
		p := guard.HandoffPathFor("/cache/dir", "abc")
		Expect(p).To(Equal(filepath.Join("/cache/dir", "abc-handoff.md")))
	})
})
