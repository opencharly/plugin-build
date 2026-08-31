package build

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
)

// storeBloatReclaimableThreshold is the reclaimable-bytes threshold (in bytes) above which a
// podman store is considered bloated enough to warn about before a build. The value comes from
// opencharly/charly#173: the overlay-store corruption reproduced even with fully serialized
// intra-build stages (Arm 3: 6/13 runs at podman_jobs: 1), which implicated the second measured
// factor — a massively bloated store (3552 images, 132.6GB, 97% reclaimable at filing; the
// development host still shows ~106GB reclaimable today). A store that bloated is the co-factor
// the corruption class tracks, and `charly clean --deep` is the purge that addresses it.
const storeBloatReclaimableThreshold = int64(50 * 1024 * 1024 * 1024) // 50 GiB

// storeDfEntry is the per-type shape of `podman system df --format json` (an array of these).
type storeDfEntry struct {
	Type           string `json:"Type"`
	RawSize        int64  `json:"RawSize"`
	RawReclaimable int64  `json:"RawReclaimable"`
}

// parseStoreReclaimable extracts the Images entry's raw reclaimable bytes from a
// `podman system df --format json` payload. Returns (reclaimable, total, nil) for the Images
// type, or an error when the payload is unparseable or carries no Images entry.
func parseStoreReclaimable(payload []byte) (reclaimable, total int64, err error) {
	var entries []storeDfEntry
	if err := json.Unmarshal(payload, &entries); err != nil {
		return 0, 0, fmt.Errorf("parse podman system df: %w", err)
	}
	for _, e := range entries {
		if e.Type == "Images" {
			return e.RawReclaimable, e.RawSize, nil
		}
	}
	return 0, 0, fmt.Errorf("parse podman system df: no Images entry")
}

// warnIfStoreBloated runs `podman system df --format json` and, when the store's reclaimable
// bytes exceed storeBloatReclaimableThreshold, prints a warning naming the purge that addresses
// it (`charly clean --deep`). It is fail-soft by design: a store probe that fails (podman absent,
// non-JSON output, no Images entry) is skipped silently — the warning is a hygiene nudge, never a
// build blocker (R4: no retry loops, no sleeps; the probe runs exactly once per build drive).
func warnIfStoreBloated(engine string, stderr io.Writer) {
	if engine != "podman" {
		return
	}
	out, err := exec.Command("podman", "system", "df", "--format", "json").Output()
	if err != nil {
		return
	}
	warnIfStoreBloatedFromPayload(out, stderr)
}

// warnIfStoreBloatedFromPayload is the payload-driven half of warnIfStoreBloated (split out so the
// threshold logic is unit-testable without exec'ing podman).
func warnIfStoreBloatedFromPayload(payload []byte, stderr io.Writer) {
	reclaimable, total, err := parseStoreReclaimable(payload)
	if err != nil {
		return
	}
	if reclaimable <= storeBloatReclaimableThreshold {
		return
	}
	pct := int64(0)
	if total > 0 {
		pct = reclaimable * 100 / total
	}
	fmt.Fprintf(stderr,
		"warning: podman store is bloated (%s reclaimable, ~%d%% of %s) — the overlay-store corruption class tracked in opencharly/charly#173 tracks this factor. Run `charly clean --deep` (pair with --invalidate for the fullest reclaim) before building.\n",
		humanBytes(reclaimable), pct, humanBytes(total))
}

// humanBytes renders a byte count in a compact human form (KiB/MiB/GiB/TiB).
func humanBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := unit, 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
// disposable proof: see the PR body
// store-bloat: see store_bloat.go (podman system df probe)
