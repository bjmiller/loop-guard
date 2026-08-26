class LoopGuard < Formula
  desc "Infinite-loop prevention for coding agents"
  homepage "https://github.com/brian/loop-guard"
  url "https://github.com/brian/loop-guard/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "" # fill from goreleaser checksums.txt at first real release
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", "-ldflags", "-s -w -X github.com/brian/loop-guard/internal/version.Version=#{version}", *std_go_args(output: bin/"loop-guard")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/loop-guard version")
    pipe_output("#{bin}/loop-guard record --session brewtest", '{"type":"tool","name":"echo","args":{"command":"hi"}}', 0)
  end
end
