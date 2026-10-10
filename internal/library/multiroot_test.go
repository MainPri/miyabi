package library

import (
	"context"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/movie"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/library/scan"
	"github.com/ppxb/miyabi/internal/pan"
)

// TestReconcileOfOneRootKeepsSiblingRootsAndSharedMovies pins the cleanup
// isolation of multiple scan roots: reconciling root A deletes only A's stale
// rows, keeps B's index intact, and preserves movies that still have file
// references in any mounted root.
func TestReconcileOfOneRootKeepsSiblingRootsAndSharedMovies(t *testing.T) {
	lib, _, payload := libraryFixture(t)
	ctx := t.Context()
	client := stubOf(t, lib.drive)
	previous := client.list
	client.list = func(requestCtx context.Context, token, id string, offset, limit int) (pan.FilePage, error) {
		if id == "20" {
			return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: "20", Name: "Other"}}}, nil
		}
		if previous != nil {
			return previous(requestCtx, token, id, offset, limit)
		}
		return pan.FilePage{}, nil
	}
	defer func() { client.list = previous }()
	if _, err := lib.drive.SelectDirectory(ctx, "20"); err != nil {
		t.Fatal(err)
	}
	sources := lib.drive.Sources()
	if len(sources) != 2 {
		t.Fatalf("mounted sources: %+v", sources)
	}

	film := lib.database.Movie.Create().SetCode("ABP-001").SaveX(ctx)
	// Stale row of root 10, current row of root 10, and a sibling row of root 20.
	for _, entry := range []struct{ id, root, scanID string }{
		{"a-stale", payload.Source.Directory.ID, "s1"},
		{"a-current", payload.Source.Directory.ID, "s2"},
		{"b-sibling", "20", "s1"},
	} {
		lib.database.File.Create().SetFileID(entry.id).SetName("ABP-001.mp4").SetSize(1024).
			SetAccountID(payload.Source.AccountID).SetRootID(entry.root).SetScanID(entry.scanID).
			SetMovieID(film.ID).ExecX(ctx)
	}

	task := lib.database.Task.Create().SetType("scan").SaveX(ctx)
	reconcile := domain.ScanPayload{Source: sources[0], Scan: domain.ScanProgress{Stage: "reconciling"}}
	if err := reconcileScan(ctx, lib, task.ID, "s2", &reconcile); err != nil {
		t.Fatal(err)
	}

	remaining := lib.database.File.Query().AllX(ctx)
	if len(remaining) != 2 {
		t.Fatalf("reconcile kept %d files, want the current root row and the sibling row: %+v", len(remaining), remaining)
	}
	roots := make(map[string]bool, len(remaining))
	for _, entry := range remaining {
		roots[entry.RootID+"/"+entry.FileID] = true
	}
	if !roots[payload.Source.Directory.ID+"/a-current"] || !roots["20/b-sibling"] {
		t.Fatalf("wrong rows survived: %v", roots)
	}
	if lib.database.Movie.Query().Where(movie.IDEQ(film.ID)).CountX(ctx) != 1 {
		t.Fatal("reconcile removed a movie that still has sibling-root references")
	}
}

func TestPruneSourceRemovesUnmountedIndexButKeepsSharedMovies(t *testing.T) {
	lib, _, payload := libraryFixture(t)
	ctx := t.Context()
	film := lib.database.Movie.Create().SetCode("ABP-001").SaveX(ctx)
	shared := lib.database.Movie.Create().SetCode("IPX-001").SaveX(ctx)
	for _, entry := range []struct {
		id, root string
		movieID  int
	}{
		{"removed-only", payload.Source.Directory.ID, film.ID},
		{"shared-a", payload.Source.Directory.ID, shared.ID},
		{"shared-b", "20", shared.ID},
	} {
		lib.database.File.Create().SetFileID(entry.id).SetName("video.mp4").SetSize(1024).
			SetAccountID(payload.Source.AccountID).SetRootID(entry.root).SetMovieID(entry.movieID).ExecX(ctx)
	}
	prune := func() {
		t.Helper()
		if err := scan.PruneSource(ctx, lib.database, payload.Source, export.Config{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	prune()
	remaining := lib.database.File.Query().AllX(ctx)
	if len(remaining) != 1 || remaining[0].FileID != "shared-b" {
		t.Fatalf("prune left %+v", remaining)
	}
	if lib.database.Movie.Query().Where(movie.IDEQ(film.ID)).CountX(ctx) != 0 {
		t.Fatal("prune kept a movie without any file reference")
	}
	if lib.database.Movie.Query().Where(movie.IDEQ(shared.ID)).CountX(ctx) != 1 {
		t.Fatal("prune removed a movie that other roots still reference")
	}
	// A second prune of the same source is idempotent.
	prune()
	if lib.database.File.Query().CountX(ctx) != 1 {
		t.Fatal("repeated prune changed the index")
	}
}
