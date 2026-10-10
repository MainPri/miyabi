package library

import (
	"context"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/task"
	"github.com/ppxb/miyabi/internal/pan"
	"github.com/ppxb/miyabi/internal/tasks"
)

func TestRebuildQueuesFreshScanAndDeduplicatesRepeatedRequests(t *testing.T) {
	lib, ordinary, source := libraryFixture(t)
	ctx := t.Context()
	first, err := lib.StartRebuild(ctx, "")
	if err != nil || len(first) != 1 || !first[0].Rebuild || first[0].ID == ordinary.ID || first[0].Source != source.Source {
		t.Fatalf("rebuild reused an ordinary scan: %+v %v", first, err)
	}
	for _, status := range []task.Status{task.StatusQueued, task.StatusRunning} {
		lib.database.Task.UpdateOneID(first[0].ID).SetStatus(status).ExecX(ctx)
		again, err := lib.StartRebuild(ctx, "")
		if err != nil || len(again) != 1 || again[0].ID != first[0].ID || !again[0].Rebuild {
			t.Fatalf("duplicate rebuild: %+v %v", again, err)
		}
	}
	input, err := tasks.DecodePayload[domain.ScanPayload](lib.database.Task.GetX(ctx, first[0].ID).Payload)
	if err != nil || !input.Rebuild || input.TargetID != "" {
		t.Fatalf("rebuild does not cover the full mount: %+v %v", input, err)
	}
	if err := lib.drive.ClearDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.StartRebuild(ctx, ""); err == nil {
		t.Fatal("rebuild accepted an unmounted source")
	}
}

func TestStartScanTargetsOneRootOrAll(t *testing.T) {
	lib, _, source := libraryFixture(t)
	ctx := t.Context()
	client := stubOf(t, lib.drive)
	previous := client.list
	client.list = func(ctx context.Context, token, id string, offset, limit int) (pan.FilePage, error) {
		if id == "20" {
			return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: "20", Name: "Other"}}}, nil
		}
		if previous != nil {
			return previous(ctx, token, id, offset, limit)
		}
		return pan.FilePage{}, nil
	}
	defer func() { client.list = previous }()
	if _, err := lib.drive.SelectDirectory(ctx, "20"); err != nil {
		t.Fatal(err)
	}
	selected, err := lib.StartScan(ctx, "20")
	if err != nil || len(selected) != 1 || selected[0].Source.Directory.ID != "20" {
		t.Fatalf("single-root scan: %+v %v", selected, err)
	}
	all, err := lib.StartScan(ctx, "")
	if err != nil || len(all) != 2 || all[0].Source.Directory.ID != source.Source.Directory.ID || all[1].Source.Directory.ID != "20" {
		t.Fatalf("all-roots scan: %+v %v", all, err)
	}
	if _, err := lib.StartScan(ctx, "999"); err == nil {
		t.Fatal("scan accepted an unmounted directory")
	}
}
