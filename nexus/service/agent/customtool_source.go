package agent

import (
	"context"
	"database/sql"
	"errors"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
)

// customToolSource translates the merged view of "tool catalog + policy table" into the shape the tools package wants.
//
// Why the extra layer: the tools package should only know "which tools exist and how to run them", never the table structure,
// the directory layout or the status semantics. A wrong translation affects only this one place.
//
// Since 2a, existence follows the **catalog** (customtoolstore does the merge) and the table only supplies
// status / configs: one more valid tool directory on disk is enough for the platform to see it.
type customToolSource struct {
	schema string
	store  *customtoolstore.Store
	// mode picks which definition this run reads: a production entry (MCP / RPC / endpoint / flow) reads the current version,
	// the editor's debug chat / the super agent read the draft. The zero value means draft.
	mode tools.DefinitionMode
}

func newCustomToolSource(schema string, mode tools.DefinitionMode) *customToolSource {
	return &customToolSource{
		schema: schema,
		store:  customtoolstore.New(config.StorageRootDir(), schema),
		mode:   mode,
	}
}

func (s *customToolSource) List(ctx context.Context) ([]tools.CustomToolSpec, error) {
	// A production run only lists tools that **have a current version**: the list comes from TOOL.json inside the version.
	// Reading the draft would surface "created but never released" tools in production (failing only when called), and would also
	// hide "deleted from the draft but still in the version" tools - which contradicts the definition freeze.
	var (
		list []models.CustomTool
		err  error
	)
	if s.mode.IsCurrent() {
		list, err = s.store.ListReleased()
	} else {
		list, err = s.store.ListEnabled(s.schema)
	}
	if err != nil {
		return nil, err
	}
	out := make([]tools.CustomToolSpec, 0, len(list))
	for i := range list {
		out = append(out, tools.CustomToolSpecFromModel(&list[i]))
	}
	return out, nil
}

// Get looks a tool up by short name. Missing or **not in the normal state** both return (nil, nil):
// a locked / deprecated tool is already invisible in the catalog, and letting the execution layer through would make the status column
// mere decoration.
//
// A production run reads the contract from the current version directory (the same source as ToolDir); no released version = missing.
func (s *customToolSource) Get(ctx context.Context, name string) (*tools.CustomToolSpec, error) {
	var (
		t   *models.CustomTool
		err error
	)
	if s.mode.IsCurrent() {
		t, err = s.store.Released(name)
	} else {
		t, err = s.store.Get(s.schema, name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Status != models.StatusNormal {
		return nil, nil
	}
	spec := tools.CustomToolSpecFromModel(t)
	return &spec, nil
}

// Status serves the file gate: it only reports the status without building a spec (the locked state must be answerable too, so Get
// cannot be reused).
func (s *customToolSource) Status(ctx context.Context, name string) (int, bool, error) {
	return s.store.Status(name)
}
