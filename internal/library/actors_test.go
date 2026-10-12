package library

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/ent/actor"
)

func TestActorsScopeCountsOrderingAndMovieFilter(t *testing.T) {
	lib, _, payload := libraryFixture(t)
	ctx := t.Context()
	actorX := lib.database.Actor.Create().SetProvider("javdb").SetSourceID("x").SetName("X actor").SetGender(actor.GenderFemale).SetNameZht("X演員").SetAvatar("https://image.example/x.jpg").SaveX(ctx)
	actorA := lib.database.Actor.Create().SetProvider("javdb").SetSourceID("a").SetName("A actor").SetGender(actor.GenderFemale).SaveX(ctx)
	actorLocal := lib.database.Actor.Create().SetProvider("javdb").SetSourceID("local").SetName("Local actor").SetGender(actor.GenderFemale).SaveX(ctx)
	actorOut := lib.database.Actor.Create().SetProvider("javdb").SetSourceID("out").SetName("Out actor").SetGender(actor.GenderFemale).SaveX(ctx)
	male := lib.database.Actor.Create().SetProvider("javdb").SetSourceID("male").SetName("Male actor").SetGender(actor.GenderMale).SaveX(ctx)
	unknown := lib.database.Actor.Create().SetProvider("javdb").SetSourceID("unknown").SetName("Unknown actor").SaveX(ctx)

	shared := lib.database.Movie.Create().SetCode("ABP-002").AddActors(actorX, actorA, male, unknown).SaveX(ctx)
	createFile := func(code string, movieID int, account, root string) {
		t.Helper()
		lib.database.File.Create().SetFileID(code).SetName(code + ".mp4").SetSize(1024).
			SetAccountID(account).SetRootID(root).SetMovieID(movieID).ExecX(ctx)
	}
	createFile("ABP-002", shared.ID, payload.Source.AccountID, payload.Source.Directory.ID)
	for _, entry := range []struct {
		code    string
		actors  []*ent.Actor
		account string
		root    string
	}{
		{code: "ABP-001", actors: []*ent.Actor{actorX}, account: payload.Source.AccountID, root: payload.Source.Directory.ID},
		{code: "ABP-003", actors: []*ent.Actor{actorA}, account: payload.Source.AccountID, root: payload.Source.Directory.ID},
		{code: "LOCAL-001", actors: []*ent.Actor{actorLocal}, account: domain.LocalAccountID, root: "local"},
		{code: "OUT-001", actors: []*ent.Actor{actorOut}, account: "other", root: payload.Source.Directory.ID},
	} {
		builder := lib.database.Movie.Create().SetCode(entry.code)
		for _, person := range entry.actors {
			builder = builder.AddActors(person)
		}
		film := builder.SaveX(ctx)
		createFile(entry.code, film.ID, entry.account, entry.root)
	}
	// A movie without any indexed file stays out of the actor count.
	if orphan := lib.database.Movie.Create().SetCode("ABP-004").AddActors(actorX).SaveX(ctx); orphan.ID == 0 {
		t.Fatal("orphan movie not created")
	}

	page, err := lib.Actors(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Page != 1 || page.HasMore || len(page.Actors) != 3 {
		t.Fatalf("actor page escaped the mounted scope: %#v", page)
	}
	counts := map[string]int{}
	for _, person := range page.Actors {
		counts[person.Name] = person.MovieCount
	}
	if !reflect.DeepEqual(counts, map[string]int{"A actor": 2, "X actor": 2, "Local actor": 1}) {
		t.Fatalf("unexpected actor counts: %#v", page.Actors)
	}
	if person := page.Actors[0]; person.Name != "A actor" || person.Provider != "javdb" || person.SourceID != "a" {
		t.Fatalf("actor identity lost: %#v", person)
	}
	if person := page.Actors[2]; person.Name != "Local actor" || person.NameZht != nil || person.Avatar != nil {
		t.Fatalf("optional fields leaked: %#v", person)
	}

	first, err := lib.Actors(ctx, 1, 2)
	if err != nil || len(first.Actors) != 2 || !first.HasMore || first.Actors[0].Name != "A actor" || first.Actors[1].Name != "X actor" {
		t.Fatalf("first actor page: %#v, %v", first, err)
	}
	second, err := lib.Actors(ctx, 2, 2)
	if err != nil || len(second.Actors) != 1 || second.HasMore || second.Actors[0].Name != "Local actor" {
		t.Fatalf("second actor page: %#v, %v", second, err)
	}
	if beyond, err := lib.Actors(ctx, 3, 2); err != nil || beyond.Actors == nil || len(beyond.Actors) != 0 || beyond.HasMore {
		t.Fatalf("out-of-range actor page: %#v, %v", beyond, err)
	}

	moviesOf := func(actorID int) []string {
		t.Helper()
		result, err := lib.Movies(ctx, 1, 20, actorID)
		if err != nil {
			t.Fatal(err)
		}
		codes := make([]string, 0, len(result.Movies))
		for _, item := range result.Movies {
			codes = append(codes, item.Code)
		}
		return codes
	}
	filtered, err := lib.Movies(ctx, 1, 1, actorX.ID)
	if err != nil || filtered.Total != 2 || !filtered.HasMore || len(filtered.Movies) != 1 {
		t.Fatalf("actor X first movie page: %#v, %v", filtered, err)
	}
	filtered, err = lib.Movies(ctx, 2, 1, actorX.ID)
	if err != nil || filtered.Total != 2 || filtered.HasMore || len(filtered.Movies) != 1 {
		t.Fatalf("actor X second movie page: %#v, %v", filtered, err)
	}
	if codes := moviesOf(actorX.ID); len(codes) != 2 {
		t.Fatalf("actor X movies: %v", codes)
	}
	if codes := moviesOf(actorA.ID); len(codes) != 2 {
		t.Fatalf("actor A movies: %v", codes)
	}
	if codes := moviesOf(actorOut.ID); len(codes) != 0 {
		t.Fatalf("out-of-scope actor returned movies: %v", codes)
	}
	if codes := moviesOf(male.ID); len(codes) != 1 || codes[0] != "ABP-002" {
		t.Fatalf("movie actor associations should remain intact: %v", codes)
	}
	if codes := moviesOf(unknown.ID); len(codes) != 1 || codes[0] != "ABP-002" {
		t.Fatalf("unknown actor associations should remain intact: %v", codes)
	}
	all, err := lib.Movies(ctx, 1, 20, 0)
	if err != nil || all.Total != 4 {
		t.Fatalf("unfiltered movies: %#v, %v", all, err)
	}

	// Without mounts only the local library remains.
	if err := lib.drive.ClearDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	local, err := lib.Actors(ctx, 1, 10)
	if err != nil || local.Total != 1 || len(local.Actors) != 1 || local.Actors[0].Name != "Local actor" || local.Actors[0].MovieCount != 1 {
		t.Fatalf("local-only actor page: %#v, %v", local, err)
	}
	if codes := moviesOf(0); len(codes) != 1 || codes[0] != "LOCAL-001" {
		t.Fatalf("local-only movies: %v", codes)
	}
}

func TestActorsMovieCountKeepsDistinctMovies(t *testing.T) {
	lib, _, payload := libraryFixture(t)
	ctx := t.Context()
	person := lib.database.Actor.Create().SetProvider("javdb").SetSourceID("multi").SetName("Multi part").SetGender(actor.GenderFemale).SaveX(ctx)
	film := lib.database.Movie.Create().SetCode("ABP-001").AddActors(person).SaveX(ctx)
	for part := range 2 {
		lib.database.File.Create().SetFileID(fmt.Sprint(part)).SetName("video.mp4").SetSize(1024).
			SetAccountID(payload.Source.AccountID).SetRootID(payload.Source.Directory.ID).SetMovieID(film.ID).ExecX(ctx)
	}
	page, err := lib.Actors(ctx, 1, 10)
	if err != nil || page.Total != 1 || page.Actors[0].MovieCount != 1 {
		t.Fatalf("multi-part files inflated the actor count: %#v, %v", page, err)
	}
}
