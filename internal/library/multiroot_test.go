package library

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/movie"
	"github.com/ppxb/miyabi/internal/ent/task"
	"github.com/ppxb/miyabi/internal/export"
	mediaimage "github.com/ppxb/miyabi/internal/image"
	"github.com/ppxb/miyabi/internal/library/scan"
	"github.com/ppxb/miyabi/internal/library/scrape"
	"github.com/ppxb/miyabi/internal/pan"
	"github.com/ppxb/miyabi/internal/tasks"
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

func TestRebuildReconcileSkipsMoviesRefreshedBySiblingRebuild(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		stale        bool
		otherAccount bool
		wantTasks    int
	}{
		{name: "fresh sibling rebuild", wantTasks: 1},
		{name: "files changed after scrape", stale: true, wantTasks: 2},
		{name: "other account rebuilt", otherAccount: true, wantTasks: 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
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

			shared := lib.database.Movie.Create().SetCode("ABP-001").SaveX(ctx)
			onlyB := lib.database.Movie.Create().SetCode("IPX-001").SaveX(ctx)
			lib.database.File.Create().SetFileID("a-shared").SetName("ABP-001.mp4").SetSize(1024).
				SetAccountID(payload.Source.AccountID).SetRootID(payload.Source.Directory.ID).
				SetScanID("s-b").SetMovieID(shared.ID).ExecX(ctx)
			lib.database.File.Create().SetFileID("b-shared").SetName("ABP-001.mp4").SetSize(1024).
				SetAccountID(payload.Source.AccountID).SetRootID("20").SetScanID("s-b").SetMovieID(shared.ID).ExecX(ctx)
			lib.database.File.Create().SetFileID("b-only").SetName("IPX-001.mp4").SetSize(1024).
				SetAccountID(payload.Source.AccountID).SetRootID("20").SetScanID("s-b").SetMovieID(onlyB.ID).ExecX(ctx)
			shared.Update().SetScrapeStatus(movie.ScrapeStatusDone).SetMetadataSnapshot(&domain.MetadataSnapshot{
				AccountID: payload.Source.AccountID, DirectoryID: payload.Source.Directory.ID,
				PosterVersion: mediaimage.PosterVersion,
				Videos: scrape.VideoFingerprint([]pan.File{
					{ID: "a-shared", Name: "ABP-001.mp4", Size: 1024},
					{ID: "b-shared", Name: "ABP-001.mp4", Size: 1024},
				}),
			}).ExecX(ctx)
			if scenario.stale {
				lib.database.File.Create().SetFileID("b-new").SetName("ABP-001-part2.mp4").SetSize(1024).
					SetAccountID(payload.Source.AccountID).SetRootID("20").SetScanID("s-b").SetMovieID(shared.ID).ExecX(ctx)
			}

			// The sibling root's rebuild scan was queued first and already refreshed the
			// shared movie through a completed rebuild scrape child.
			cutoff := time.Now().Add(-time.Minute)
			rebuild := domain.ScanPayload{Source: sources[1], Rebuild: true, Scan: domain.ScanProgress{Stage: "reconciling"}}
			scanB := lib.database.Task.Create().SetType("scan").SetCreatedAt(cutoff).SaveX(ctx)
			scrapeSource := sources[0]
			if scenario.otherAccount {
				scrapeSource.AccountID = "another-account"
			}
			rebuildBody, err := tasks.EncodePayload(scrape.Payload{MetadataPayload: scrape.MetadataPayload{
				Rebuild: true, Source: scrapeSource, ScanTaskID: scanB.ID, MovieID: shared.ID, Code: shared.Code,
			}, Completed: true})
			if err != nil {
				t.Fatal(err)
			}
			lib.database.Task.Create().SetType("scrape").SetStatus(task.StatusDone).
				SetResourceKey(fmt.Sprintf("movie:%d", shared.ID)).SetPayload(rebuildBody).
				SetCreatedAt(cutoff.Add(time.Second)).SaveX(ctx)
			if err := reconcileScan(ctx, lib, scanB.ID, "s-b", &rebuild); err != nil {
				t.Fatal(err)
			}

			queued := lib.database.Task.Query().Where(task.TypeEQ("scrape"), task.StatusEQ(task.StatusQueued)).AllX(ctx)
			if len(queued) != scenario.wantTasks {
				t.Fatalf("rebuild queued %d scrape tasks, want %d: %+v", len(queued), scenario.wantTasks, queued)
			}
			seen := make(map[int]bool)
			for _, job := range queued {
				input, err := tasks.DecodePayload[scrape.MetadataPayload](job.Payload)
				if err != nil || seen[input.MovieID] {
					t.Fatalf("invalid or duplicate scrape target: %+v %v", input, err)
				}
				seen[input.MovieID] = true
			}
			if !seen[onlyB.ID] || seen[shared.ID] != (scenario.wantTasks == 2) {
				t.Fatalf("wrong rebuild targets: %v", seen)
			}
		})
	}
}
