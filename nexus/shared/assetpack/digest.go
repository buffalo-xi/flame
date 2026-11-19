package assetpack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ManifestSpec is the **semantic** version of the current manifest / payload.
//
// How it divides the work with PackageFormat:
//   - PackageFormat (flame-asset/1) only guarantees "the container and metadata structure of this zip
//     can be parsed";
//   - ManifestSpec covers "do I understand the fields that came out of it": adding a field or an enum
//     value to the manifest or payload, or changing its meaning, requires +1, and the importing side
//     uses it to reject packages "newer than this program" outright instead of silently dropping
//     fields it does not know and installing an entity that behaves wrongly.
//
// The rule: a missing spec (an old package has no such field) counts as 1; spec > ManifestSpec means
// the install is refused.
const ManifestSpec = 1

// StampManifest fills in Spec and Digest (idempotent: an existing Digest stays the same while the
// content does not change).
//
// All three paths that produce an asset package — listing, export and the cross-site pull — must call
// it, otherwise the same package gets different fingerprints through different paths and the "is the
// content the same" decision is immediately wrong.
func StampManifest(m *Manifest, payload []byte) error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if m.Spec <= 0 {
		m.Spec = ManifestSpec
	}
	d, err := digestOf(*m, payload)
	if err != nil {
		return err
	}
	m.Digest = d
	return nil
}

// digestOf computes the content fingerprint: payload (normalised JSON) plus the dependency
// declarations plus the file manifest (sorted by path).
//
// It deliberately includes "name / dependencies / file paths and digests" and **not** timestamps,
// origin site or build number — if those changed the fingerprint, "the same content listed again"
// would count as new content. Normalising the JSON is essential: the payload is a map, a plain
// Marshal would follow Go's key order, and two payloads with the same meaning but different bytes
// have to produce the same fingerprint.
func digestOf(m Manifest, payload []byte) (string, error) {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return "", fmt.Errorf("payload is not valid JSON: %w", err)
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to normalise the payload: %w", err)
	}
	deps, err := json.Marshal(normalizeDeps(m.Deps))
	if err != nil {
		return "", fmt.Errorf("failed to serialise the dependency declarations: %w", err)
	}

	h := sha256.New()
	fmt.Fprintf(h, "flame-asset-digest/1\x00kind=%s\x00name=%s\x00entry=%s\x00", m.Kind, m.Name, m.Entry)
	h.Write(canonical)
	h.Write([]byte{0})
	h.Write(deps)

	// The file manifest is sorted by path before hashing: a different directory traversal order does
	// not mean different content
	files := append([]FileEntry(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		fmt.Fprintf(h, "\x00%s\x00%d\x00%s", f.Path, f.Size, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// normalizeDeps puts the dependency declarations into a stable order: Tools / Skills / Agents /
// Requires are all sets semantically, so a different declaration order does not mean different
// content. Sorting only happens on a copy — the manifest is stored and displayed as declared.
func normalizeDeps(d Deps) Deps {
	d.Tools = append([]ToolDep(nil), d.Tools...)
	sort.Slice(d.Tools, func(i, j int) bool {
		a, b := d.Tools[i], d.Tools[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.GrantKind < b.GrantKind
	})
	d.Skills = sortedCopy(d.Skills)
	d.PrivateSkills = sortedCopy(d.PrivateSkills)
	d.Agents = sortedCopy(d.Agents)
	d.Requires = sortedCopy(d.Requires)
	d.GlobalVars = sortedCopy(d.GlobalVars)
	return d
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

// ShortDigest is the display form of a fingerprint (first 8 characters). An empty fingerprint returns
// the empty string: old packages have none, and it must not show up as "00000000".
func ShortDigest(d string) string {
	if len(d) < 8 {
		return d
	}
	return d[:8]
}
