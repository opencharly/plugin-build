package build

// ref_reuse.go — the REF DECISION for a built image (opencharly/charly#1292, option B).
//
// The drive always builds. When the freshly built artifact is BYTE-IDENTICAL to the newest
// local build of the same box it publishes that build's existing ref instead of minting a new
// CalVer; when the artifact changed it mints a fresh one (`mintAutoRef`: the calendar minute,
// or that minute with a `-N` suffix when a DIFFERENT artifact already owns it). The predicate
// is the artifact's own content address (the image config digest), never the build clock: a
// tag change alone with the same digest is a BuildKit cache hit, and a rebuild with a changed
// digest MUST get a new ref (silently re-pointing an existing tag at different bytes is worse
// than churn).
//
// Scope: a ref whose tag was EXPLICITLY requested (`--tag`) is the caller's to name and is
// never replaced; a push target's ref is baked into the buildctl argv (the push exporter)
// before the build, and a mirrored build pushes the provisional ref to the local registry, so
// both keep the resolve-time ref. Only a local, unnamed build decides after the build.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/storekit"
	"github.com/opencharly/spec/spec"
)

// localRef is the newest local ref for a box's repository, with the artifact it names.
type localRef struct {
	Ref      string
	ImageID  string
	StoreHas bool // the charly image store holds it (the build-time binding needs it there)
}

// refRepo strips the tag from a full ref, preserving a registry port colon.
func refRepo(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i]
	}
	return ref
}

// chooseBuiltRef is the PURE decision behind option B: given the box's resolve-time ref, the
// freshly built image ID, the caller's explicit tag (if any), whether the box is a push
// target, the engine's local images and a store-presence predicate, return the ref to publish
// and whether it is a reuse of an existing artifact.
//
// It compares against the NEWEST local ref of the same repository: reusing an OLDER tag while
// a newer, different image sits on top would make `charly update`'s newest-local resolution
// deploy the other image. Comparing against the newest makes "reuse" mean "newest already IS
// this artifact", and a changed artifact mints a ref that becomes the newest.
func chooseBuiltRef(fullTag, newImageID string, autoTag, pushTarget bool, locals []kit.LocalImageInfo, storeHas func(string) bool) (ref string, reused bool, prev localRef) {
	repo := refRepo(fullTag)
	var bestRef, bestID string
	var bestCreated int64
	for _, img := range locals {
		for _, name := range img.Names {
			if refRepo(name) != repo {
				continue
			}
			if bestRef == "" || img.Created > bestCreated || (img.Created == bestCreated && refTag(name) > refTag(bestRef)) {
				bestRef, bestID, bestCreated = name, img.ID, img.Created
			}
		}
	}
	if bestRef != "" {
		prev = localRef{Ref: bestRef, ImageID: bestID, StoreHas: storeHas(bestRef)}
	}
	// 1. A caller-supplied --tag names the artifact: never replace it. The CLI always sends
	//    a computed CalVer, so "caller-supplied" is recognised by the TAG GRAMMAR: a bare
	//    CalVer is the drive's own auto tag; anything else (a bed/release/user tag) is the
	//    caller's and is kept verbatim.
	if !autoTag {
		return fullTag, false, prev
	}
	// 2. A push target's ref is already in the buildctl argv.
	if pushTarget {
		return fullTag, false, prev
	}
	// 3. Reuse the newest local ref only when it is the SAME artifact and the store holds it
	//    (the store is what binds a locally built stage for the next box).
	// The engine reports an image ID as bare hex (`podman images --format json` .Id),
	// while buildkitdrive.ImageID reports the config digest with the `sha256:` prefix.
	if bestRef != "" && sameDigest(bestID, newImageID) && prev.StoreHas {
		return bestRef, true, prev
	}
	// 4. The artifact is new: mint a fresh CalVer, bumping the minute when the computed tag
	//    is already taken by a DIFFERENT artifact (two changed builds inside one minute
	//    would otherwise overwrite each other's tag — the churn the reuse predicate exists
	//    to stop, in its worst form: a tag silently re-pointed at different bytes).
	return mintAutoRef(repo, buildkit.ComputeCalVer(), newImageID, locals, storeHas), false, prev
}

// mintAutoRef returns the ref for a NEW artifact: the CalVer tag when it is free, else the
// same CalVer with a numeric suffix (`-2`, `-3`, ...). A tag is TAKEN when the engine or the
// charly store already names it for a different content address; when it names the SAME
// artifact the ref is that artifact's and is returned unchanged.
//
// The bump is what keeps the predicate's own promise ("never re-point a tag at different
// bytes") true across the minute boundary: two changed builds inside one minute produce
// `...:HHMM` and `...:HHMM-2`, not one tag written twice.
func mintAutoRef(repo, base, newImageID string, locals []kit.LocalImageInfo, storeHas func(string) bool) string {
	for n := 1; n <= 99; n++ {
		tag := base
		if n > 1 {
			tag = fmt.Sprintf("%s-%d", base, n)
		}
		ref := repo + ":" + tag
		if id, ok := engineRefID(locals, ref); ok {
			if sameDigest(id, newImageID) {
				return ref // already this artifact: nothing to distinguish
			}
			continue // a different artifact owns the minute: bump
		}
		if storeHas(ref) {
			continue // the store holds it for another payload
		}
		return ref
	}
	// Pathological (99 changed builds in one minute): stay unique rather than overwrite.
	return fmt.Sprintf("%s:%s-%s", repo, base, shortDigest(newImageID))
}

// engineRefID returns the image ID the engine reports for an exact ref name.
func engineRefID(locals []kit.LocalImageInfo, ref string) (string, bool) {
	for _, img := range locals {
		for _, name := range img.Names {
			if name == ref {
				return img.ID, true
			}
		}
	}
	return "", false
}

// shortDigest is the 12-hex content suffix used only when every numeric slot is taken.
func shortDigest(imageID string) string {
	d := strings.TrimPrefix(imageID, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

// isAutoTag reports whether a box's tag is the drive's own auto CalVer (YYYY.DDD.HHMM), as
// opposed to a caller-supplied tag (a bed tag like check-openwebui-pod-2026.331.0504, a
// release tag, a user tag), which names the artifact and is never replaced.
func isAutoTag(fullTag string) bool {
	tag := refTag(fullTag)
	// A collision-bumped auto tag (`YYYY.DDD.HHMM-2`) is still the drive's own: split the
	// numeric suffix off and grammar-check the CalVer prefix.
	if i := strings.IndexByte(tag, '-'); i >= 0 {
		return isCalVer(tag[:i]) && isDigits(tag[i+1:])
	}
	return isCalVer(tag)
}

// isCalVer reports whether tag is the drive's bare CalVer form, YYYY.DDD.HHMM.
func isCalVer(tag string) bool {
	if len(tag) != 13 { // 4 + 1 + 3 + 1 + 4
		return false
	}
	for i, c := range tag {
		switch i {
		case 4, 8:
			if c != '.' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// sameDigest compares two content addresses that may differ only in their `sha256:` prefix.
func sameDigest(a, b string) bool {
	return strings.TrimPrefix(a, "sha256:") == strings.TrimPrefix(b, "sha256:")
}

// decideRef gathers the engine's images and the charly store, then applies chooseBuiltRef.
func (c driveConfig) decideRef(box spec.BuildResolveBox, newImageID string, reuse bool) (string, bool, localRef, error) {
	// A direct caller of buildImage owns the ref (the live bridge tests pass one); a push
	// target's ref is baked into the argv before the build; a mirrored build pushes the
	// provisional ref to the local registry. All three keep the resolve-time ref, no reuse.
	if !reuse || (c.Push && c.PushTargets[box.Name]) || c.RegistryMirror != "" {
		return box.FullTag, false, localRef{}, nil
	}
	locals, err := kit.ListLocalImages(c.RunEngine)
	if err != nil {
		return "", false, localRef{}, fmt.Errorf("listing local images for %s: %w", box.Name, err)
	}
	store, err := storekit.OpenImageStore()
	if err != nil {
		return "", false, localRef{}, fmt.Errorf("opening the charly image store: %w", err)
	}
	storeHas := func(ref string) bool {
		ok, herr := store.Has(ref)
		return herr == nil && ok
	}
	ref, reused, prev := chooseBuiltRef(box.FullTag, newImageID, true, false, locals, storeHas)
	return ref, reused, prev, nil
}

// applyRefSubstitutions rewrites a child's resolve-time refs to the refs the drive actually
// published for boxes it already built. Only the tag-bearing tokens (`ARG BASE_IMAGE=` and a
// builder `FROM`) contain them, so the rewrite is exact; replacements are token-bounded so a
// tag that is a prefix of another (`:probe-1` vs `:probe-12`) is never corrupted.
func applyRefSubstitutions(containerfile string, decided map[string]string) string {
	if len(decided) == 0 {
		return containerfile
	}
	olds := make([]string, 0, len(decided))
	for old := range decided {
		olds = append(olds, old)
	}
	// Longest first: a longer ref can contain a shorter one only at a tag-prefix boundary,
	// which replaceRefToken already rejects, but the order keeps the intent explicit.
	sort.Slice(olds, func(i, j int) bool { return len(olds[i]) > len(olds[j]) })
	for _, old := range olds {
		final := decided[old]
		if old == "" || old == final {
			continue
		}
		containerfile = replaceRefToken(containerfile, old, final)
	}
	return containerfile
}

// replaceRefToken replaces old with final only where old ends at a token boundary.
func replaceRefToken(s, old, final string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(old)
		if end < len(s) && isRefTagChar(s[end]) {
			// A longer tag starts with old: keep the bytes and look further.
			b.WriteString(s[:end])
			s = s[end:]
			continue
		}
		b.WriteString(s[:i])
		b.WriteString(final)
		s = s[end:]
	}
}

func isRefTagChar(c byte) bool {
	return c == '.' || c == '-' || c == '_' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
