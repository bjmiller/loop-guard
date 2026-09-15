# Homebrew formula for loop-guard.
#
# This is a template mirroring what `goreleaser release` generates for the
# homebrew-loop-guard tap (see .goreleaser.yaml, `brews:`); it installs the
# prebuilt release archives. The sha256 values are filled from checksums.txt
# at the first real release. Until then, install with
# `go install github.com/bjmiller/loop-guard@latest` or a local build.
class LoopGuard < Formula
  desc "Infinite-loop prevention for coding agents"
  homepage "https://github.com/bjmiller/loop-guard"
  version "0.1.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/bjmiller/loop-guard/releases/download/v#{version}/loop-guard_#{version}_darwin_amd64.tar.gz"
      sha256 ""
    end
    if Hardware::CPU.arm?
      url "https://github.com/bjmiller/loop-guard/releases/download/v#{version}/loop-guard_#{version}_darwin_arm64.tar.gz"
      sha256 ""
    end
  end

  on_linux do
    if Hardware::CPU.intel?
      url "https://github.com/bjmiller/loop-guard/releases/download/v#{version}/loop-guard_#{version}_linux_amd64.tar.gz"
      sha256 ""
    end
    if Hardware::CPU.arm?
      url "https://github.com/bjmiller/loop-guard/releases/download/v#{version}/loop-guard_#{version}_linux_arm64.tar.gz"
      sha256 ""
    end
  end

  def install
    bin.install "loop-guard"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/loop-guard version")
    pipe_output(
      "#{bin}/loop-guard record --session brewtest",
      '{"type":"tool","name":"echo","args":{"command":"hi"}}',
      0,
    )
  end
end
