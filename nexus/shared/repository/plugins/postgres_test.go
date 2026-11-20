package plugins

import (
	"strings"
	"testing"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// Upsert must refuse an empty kind.
//
// The database's DEFAULT 'tools' only applies when the column is **not passed explicitly**; once an empty string lands in
// the kind column, that row can never match any kind-filtered query again — it shows up as "the save succeeded but the
// plugin vanished", one of the hardest failures to diagnose. The check happens before touching the database, so this test
// needs no connection.
func TestUpsertRejectsEmptyKind(t *testing.T) {
	err := NewRepository().Upsert(&auth.AuthInfo{Schema: "t0"}, &models.Plugin{
		Kind: "   ",
		Name: "web_search",
	})
	if err == nil {
		t.Fatal("an empty kind should error, but nil was returned (it would be stored as an empty kind)")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("the error should mention kind so it can be located, got: %v", err)
	}
}

func TestUpsertRejectsNilPlugin(t *testing.T) {
	if err := NewRepository().Upsert(&auth.AuthInfo{Schema: "t0"}, nil); err == nil {
		t.Fatal("a nil plugin should error")
	}
}
