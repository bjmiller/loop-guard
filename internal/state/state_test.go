package state_test

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/brian/loop-guard/internal/state"
)

var _ = Describe("Store", func() {
	var dir string
	var clock *state.FakeClock
	var store *state.Store

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		clock = state.NewFakeClock(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
		store = &state.Store{Dir: dir, Clock: clock}
	})

	Describe("Update", func() {
		It("creates a fresh session on first use", func() {
			err := store.Update("s1", func(s *state.Session) {
				s.Events = append(s.Events, state.Event{Type: "response", Text: "hello"})
			})
			Expect(err).NotTo(HaveOccurred())

			s, err := store.Load("s1")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ID).To(Equal("s1"))
			Expect(s.Version).To(Equal(state.CurrentVersion))
			Expect(s.Events).To(HaveLen(1))
			Expect(s.CreatedAt.Equal(clock.Now())).To(BeTrue())
		})

		It("persists mutations across loads", func() {
			Expect(store.Update("s1", func(s *state.Session) { s.Interventions = 2 })).To(Succeed())
			Expect(store.Update("s1", func(s *state.Session) { s.Interventions++ })).To(Succeed())

			s, err := store.Load("s1")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Interventions).To(Equal(3))
		})

		It("stamps UpdatedAt with the clock", func() {
			Expect(store.Update("s1", func(s *state.Session) {})).To(Succeed())
			s, _ := store.Load("s1")
			Expect(s.UpdatedAt.Equal(clock.Now())).To(BeTrue())
		})

		It("caps stored events", func() {
			Expect(store.Update("cap", func(s *state.Session) {
				for i := 0; i < state.MaxEvents+10; i++ {
					s.Events = append(s.Events, state.Event{Type: "tool", Fingerprint: "f"})
				}
			})).To(Succeed())
			s, err := store.Load("cap")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Events).To(HaveLen(state.MaxEvents))
		})

		It("recovers from a corrupt state file by starting fresh", func() {
			path := filepath.Join(dir, "broken.json")
			Expect(os.WriteFile(path, []byte("{not json"), 0o600)).To(Succeed())

			Expect(store.Update("broken", func(s *state.Session) {
				s.Events = append(s.Events, state.Event{Type: "tool"})
			})).To(Succeed())

			s, err := store.Load("broken")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Events).To(HaveLen(1))
		})
	})

	Describe("locking", func() {
		It("serializes concurrent updates without losing writes", func() {
			const writers = 20
			var wg sync.WaitGroup
			for i := 0; i < writers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer GinkgoRecover()
					Expect(store.Update("conc", func(s *state.Session) {
						s.Interventions++
					})).To(Succeed())
				}()
			}
			wg.Wait()

			s, err := store.Load("conc")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Interventions).To(Equal(writers))
		})

		It("steals a stale lock left by a crashed process", func() {
			lockPath := filepath.Join(dir, "stale.lock")
			stale := time.Now().Add(-state.StaleLockAge - time.Minute)
			Expect(os.WriteFile(lockPath, nil, 0o600)).To(Succeed())
			Expect(os.Chtimes(lockPath, stale, stale)).To(Succeed())

			Expect(store.Update("stale", func(s *state.Session) {})).To(Succeed())
		})

		It("removes the lock file after updating", func() {
			Expect(store.Update("lockclean", func(s *state.Session) {})).To(Succeed())
			_, err := os.Stat(filepath.Join(dir, "lockclean.lock"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})
})

var _ = Describe("SessionStatePath", func() {
	It("joins the directory and session id", func() {
		Expect(state.SessionStatePath("/cache", "abc")).To(Equal(filepath.Join("/cache", "abc.json")))
	})
})

var _ = Describe("DefaultDir", func() {
	It("honors the LOOPGUARD_CACHE_DIR override", func() {
		DeferCleanup(os.Unsetenv, "LOOPGUARD_CACHE_DIR")
		Expect(os.Setenv("LOOPGUARD_CACHE_DIR", "/custom/cache")).To(Succeed())
		dir, err := state.DefaultDir()
		Expect(err).NotTo(HaveOccurred())
		Expect(dir).To(Equal(filepath.Join("/custom/cache")))
	})

	It("falls back to the user cache dir plus loop-guard", func() {
		DeferCleanup(os.Unsetenv, "LOOPGUARD_CACHE_DIR")
		Expect(os.Unsetenv("LOOPGUARD_CACHE_DIR")).To(Succeed())
		base, err := os.UserCacheDir()
		if err != nil {
			Skip("no user cache dir available")
		}
		dir, err := state.DefaultDir()
		Expect(err).NotTo(HaveOccurred())
		Expect(dir).To(Equal(filepath.Join(base, "loop-guard")))
	})
})
