package versioning

import (
	"context"
	"reflect"
	"testing"

	"flame/nexus/shared/storage"
)

// When loading an old agent version, a private skill that exists in the draft but not in the version must be called out
// and kept. Missing it would let it be deleted as "an extra file outside the version" — while it is an entity of its own
// (it has a row in the skills table), and deleting the directory turns it into an empty skill of "a row but no body".
func TestKeptPrivateSkills(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()
	draft := "tenants/t0/agents/poet"

	for _, name := range []string{"verse", "lyric"} {
		key := draft + "/skills/" + name + "/SKILL.md"
		if err := st.WriteFile(ctx, key, []byte("# "+name)); err != nil {
			t.Fatalf("prepare %s: %v", key, err)
		}
	}

	// Only verse was frozen into the version
	got, err := KeptPrivateSkills(ctx, st, draft, []byte(`[{"name":"verse"}]`))
	if err != nil {
		t.Fatalf("KeptPrivateSkills: %v", err)
	}
	if want := []string{"lyric"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KeptPrivateSkills = %v, want %v", got, want)
	}

	// Empty skills snapshot: both count as "not in the version"
	got, _ = KeptPrivateSkills(ctx, st, draft, []byte(`[]`))
	if want := []string{"lyric", "verse"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("empty snapshot KeptPrivateSkills = %v, want %v", got, want)
	}

	// The draft has no skills/ directory at all: nothing to keep, and it must not fail
	if got, err := KeptPrivateSkills(ctx, st, "tenants/t0/agents/none", []byte(`[]`)); err != nil || got != nil {
		t.Fatalf("no skills directory should return (nil, nil), got (%v, %v)", got, err)
	}
}
