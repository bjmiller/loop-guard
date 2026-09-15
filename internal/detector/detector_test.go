package detector_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bjmiller/loop-guard/internal/detector"
)

var _ = Describe("ToolFingerprint", func() {
	It("is stable for identical inputs", func() {
		a := detector.ToolFingerprint("bash", map[string]any{"command": "ls -la"})
		b := detector.ToolFingerprint("bash", map[string]any{"command": "ls -la"})
		Expect(a).To(Equal(b))
	})

	It("normalizes key order in args", func() {
		a := detector.ToolFingerprint("write", map[string]any{"path": "/tmp/x", "content": "hi"})
		b := detector.ToolFingerprint("write", map[string]any{"content": "hi", "path": "/tmp/x"})
		Expect(a).To(Equal(b))
	})

	It("distinguishes different tools and args", func() {
		a := detector.ToolFingerprint("bash", map[string]any{"command": "ls"})
		b := detector.ToolFingerprint("grep", map[string]any{"command": "ls"})
		c := detector.ToolFingerprint("bash", map[string]any{"command": "pwd"})
		Expect(a).NotTo(Equal(b))
		Expect(a).NotTo(Equal(c))
	})

	It("handles non-map args including nil and strings", func() {
		Expect(detector.ToolFingerprint("t", nil)).NotTo(BeEmpty())
		Expect(detector.ToolFingerprint("t", "raw string")).NotTo(BeEmpty())
	})
})

var _ = Describe("Similarity", func() {
	It("returns 1 for identical text", func() {
		Expect(detector.Similarity("the quick brown fox", "the quick brown fox")).To(Equal(1.0))
	})

	It("ignores case, punctuation, and whitespace differences", func() {
		Expect(detector.Similarity("Error: file not found!", "error   FILE not FOUND")).To(Equal(1.0))
	})

	It("returns 0 for disjoint text", func() {
		Expect(detector.Similarity("alpha beta gamma", "delta epsilon zeta")).To(Equal(0.0))
	})

	It("scores partial overlap between 0 and 1", func() {
		s := detector.Similarity("retry the build now please", "retry the tests now please")
		Expect(s).To(BeNumerically(">", 0.5))
		Expect(s).To(BeNumerically("<", 1.0))
	})

	It("treats two empty strings as identical", func() {
		Expect(detector.Similarity("", "")).To(Equal(1.0))
	})
})

var _ = Describe("DetectToolLoop", func() {
	cfg := detector.Config{ToolThreshold: 3, ToolWindow: 6}

	It("does not flag a fresh fingerprint", func() {
		seq := []string{"a", "b", "c"}
		Expect(detector.DetectToolLoop(seq, cfg).Loop).To(BeFalse())
	})

	It("flags when the current fingerprint repeats threshold times within window", func() {
		seq := []string{"x", "y", "x", "z", "x"}
		v := detector.DetectToolLoop(seq, cfg)
		Expect(v.Loop).To(BeTrue())
		Expect(v.Kind).To(Equal("tool"))
		Expect(v.Count).To(Equal(3))
	})

	It("ignores repetitions that fall outside the window", func() {
		seq := []string{"x", "a", "b", "c", "d"}
		Expect(detector.DetectToolLoop(seq, cfg).Loop).To(BeFalse())
	})

	It("counts only occurrences inside the window", func() {
		seq := []string{"x", "a", "b", "x", "c", "x"}
		v := detector.DetectToolLoop(seq, cfg)
		Expect(v.Loop).To(BeTrue())
		Expect(v.Count).To(Equal(3))
	})

	It("respects a custom threshold of 2", func() {
		cfg2 := detector.Config{ToolThreshold: 2, ToolWindow: 4}
		Expect(detector.DetectToolLoop([]string{"q", "r", "q"}, cfg2).Loop).To(BeTrue())
		Expect(detector.DetectToolLoop([]string{"q", "r", "s"}, cfg2).Loop).To(BeFalse())
	})

	It("handles empty sequences", func() {
		Expect(detector.DetectToolLoop(nil, cfg).Loop).To(BeFalse())
	})
})

var _ = Describe("DetectResponseLoop", func() {
	cfg := detector.Config{ResponseThreshold: 3, ResponseWindow: 6}

	longText := "The build failed because the test suite timed out after thirty seconds of waiting."

	It("does not flag dissimilar responses", func() {
		seq := []string{longText, "Completely unrelated words about cooking pasta tonight."}
		Expect(detector.DetectResponseLoop(seq, cfg).Loop).To(BeFalse())
	})

	It("flags near-duplicate long responses once enough accumulate", func() {
		seq := []string{longText,
			"The build FAILED because the test suite TIMED OUT after thirty seconds!",
			"the build failed because the test suite timed out after thirty seconds of waiting"}
		v := detector.DetectResponseLoop(seq, cfg)
		Expect(v.Loop).To(BeTrue())
		Expect(v.Kind).To(Equal("response"))
		Expect(v.Count).To(Equal(3))
	})

	It("does not flag short repeated acknowledgements", func() {
		seq := []string{"Done.", "Done.", "Done."}
		Expect(detector.DetectResponseLoop(seq, cfg).Loop).To(BeFalse())
	})

	It("ignores similar responses outside the window", func() {
		pad := []string{"unrelated alpha", "unrelated beta", "unrelated gamma", "unrelated delta",
			"unrelated epsilon", "unrelated zeta", "unrelated eta", "unrelated theta"}
		seq := append(pad, longText)
		Expect(detector.DetectResponseLoop(seq, cfg).Loop).To(BeFalse())
	})

	It("respects a custom similarity threshold", func() {
		variant := "The build failed because tests timed out."
		strict := detector.Config{ResponseThreshold: 2, ResponseWindow: 6, Similarity: 0.999}
		seq := []string{longText, variant}
		Expect(detector.DetectResponseLoop(seq, strict).Loop).To(BeFalse())

		lenient := detector.Config{ResponseThreshold: 2, ResponseWindow: 6, Similarity: 0.3}
		Expect(detector.DetectResponseLoop(seq, lenient).Loop).To(BeTrue())
	})

	It("handles empty sequences", func() {
		Expect(detector.DetectResponseLoop(nil, cfg).Loop).To(BeFalse())
	})
})
