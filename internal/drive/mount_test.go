package drive

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/pan"
)

func mountFixture(t *testing.T) (*Drive, *stubClient) {
	t.Helper()
	client := &stubClient{}
	d := newTestDrive(t, client)
	loginTestAccount(t, d)
	return d, client
}

// mountRecorder observes mount events and can reject a mount.
type mountRecorder struct {
	mu      sync.Mutex
	calls   int
	last    MountEvent
	failure error
}

func recordMounts(d *Drive) *mountRecorder {
	r := &mountRecorder{}
	d.SetMountListener(func(ctx context.Context, event MountEvent) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls++
		r.last = event
		return r.failure
	})
	return r
}

func (r *mountRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *mountRecorder) lastEvent() MountEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *mountRecorder) reject(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failure = err
}

func TestMountListenerReplacementAndClearing(t *testing.T) {
	d, _ := mountFixture(t)
	d.SetMountListener(func(context.Context, MountEvent) error {
		return errors.New("replaced listener must not run")
	})
	var calls atomic.Int32
	d.SetMountListener(func(_ context.Context, event MountEvent) error {
		calls.Add(1)
		// Reading state and clearing the callback must not deadlock on d.mu.
		mounted := slices.ContainsFunc(d.Sources(), func(source domain.LibrarySource) bool { return source == event.Source })
		if !mounted {
			return errors.New("listener did not observe the new mount")
		}
		d.SetMountListener(nil)
		return nil
	})
	finished := make(chan error, 1)
	go func() {
		_, err := d.SelectDirectory(t.Context(), "20")
		finished <- err
	}()
	if err := await(t, finished); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearDirectory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(d.Sources()) != 0 {
		t.Fatalf("cleared listener: calls=%d sources=%+v", calls.Load(), d.Sources())
	}
}

func TestDirectoryMountPublishesOnceAndDoesNotInterruptTheSameMount(t *testing.T) {
	d, _ := mountFixture(t)
	ctx := t.Context()
	mounts := recordMounts(d)
	directory, err := d.SelectDirectory(ctx, "20")
	if err != nil || directory.ID != "20" || directory.Path != "/Movies20" {
		t.Fatalf("mount: %+v err=%v", directory, err)
	}
	event := mounts.lastEvent()
	if mounts.count() != 1 || event.Change != MountAdded || event.Source.AccountID != testSource.AccountID || event.Source.Directory != directory {
		t.Fatalf("mount did not publish its source once: calls=%d event=%+v", mounts.count(), event)
	}
	version := d.snapshot().authorizationVersion
	for range 3 {
		if _, err := d.SelectDirectory(ctx, "20"); err != nil {
			t.Fatal(err)
		}
	}
	if d.snapshot().authorizationVersion != version || mounts.count() != 1 {
		t.Fatal("repeated mount interrupted active work or published again")
	}
	if _, err := d.SelectDirectory(ctx, "30"); err != nil {
		t.Fatal(err)
	}
	if d.snapshot().authorizationVersion != version+1 || mounts.count() != 2 {
		t.Fatal("adding a directory did not publish exactly one new mount")
	}
	mounted := d.Sources()
	if len(mounted) != 2 || mounted[0].Directory.ID != "20" || mounted[1].Directory.ID != "30" {
		t.Fatalf("mounted sources = %+v", mounted)
	}
	// Startup restores the durable mounts without publishing.
	restarted, err := NewWithClient(ctx, d.database, d.client)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restored := restarted.Sources()
	if len(restored) != 2 || restored[0].Directory.ID != "20" || restored[1].Directory.ID != "30" {
		t.Fatalf("restart lost the mounts: %+v", restored)
	}
}

func TestSelectDirectoryRejectsOverlappingRoots(t *testing.T) {
	d, client := mountFixture(t)
	ctx := t.Context()
	// The chain answers carry the ancestry 115 returns for each listing.
	client.list = func(_ context.Context, _, id string, _, _ int) (pan.FilePage, error) {
		switch id {
		case "nested":
			return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: "10", Name: "Movies"}, {ID: "nested", Name: "Nested"}}}, nil
		case "10":
			return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: "root-of-10", Name: "Root"}, {ID: "10", Name: "Movies"}}}, nil
		default:
			return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: id, Name: "Movies" + id}}}, nil
		}
	}
	if _, err := d.SelectDirectory(ctx, "10"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SelectDirectory(ctx, "nested"); err == nil {
		t.Fatal("mounting a directory inside an existing root was accepted")
	}
	// Mounting the ancestor of an existing root is rejected through its chain.
	if _, err := d.SelectDirectory(ctx, "root-of-10"); err == nil {
		t.Fatal("mounting an ancestor of an existing root was accepted")
	}
	if len(d.Sources()) != 1 || d.Sources()[0].Directory.ID != "10" {
		t.Fatalf("overlap checks changed the mount set: %+v", d.Sources())
	}
}

func TestMountCachesAncestryAndListsOnlyTheNewDirectory(t *testing.T) {
	d, client := mountFixture(t)
	ctx := t.Context()
	calls := make(map[string]int)
	client.list = func(_ context.Context, _, id string, _, _ int) (pan.FilePage, error) {
		calls[id]++
		return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: id, Name: "Movies" + id}}}, nil
	}
	for _, id := range []string{"10", "20", "30"} {
		if _, err := d.SelectDirectory(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 3 || calls["10"] != 1 || calls["20"] != 1 || calls["30"] != 1 {
		t.Fatalf("cached mounts triggered extra 115 listings: %v", calls)
	}
	saved, found := savedRoots(t, d)
	if !found || len(saved) != 3 {
		t.Fatalf("mount records not saved: %+v", saved)
	}
	for _, entry := range saved {
		if !slices.Equal(entry.Ancestors, []string{"0", entry.ID}) {
			t.Fatalf("mount %s lost its ancestry: %v", entry.ID, entry.Ancestors)
		}
	}
	restarted, err := NewWithClient(ctx, d.database, d.client)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err := restarted.SelectDirectory(ctx, "40"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls["40"] != 1 {
		t.Fatalf("restart re-listed cached roots: %v", calls)
	}
}

func TestMountLegacyRecordFallsBackToListingForOverlap(t *testing.T) {
	d, client := mountFixture(t)
	ctx := t.Context()
	selectTestDirectory(t, d, client, domain.LibraryDirectory{ID: "10", Name: "Movies10", Path: "/Movies10"})
	legacy := slices.Clone(d.snapshot().roots)
	legacy[0].Ancestors = nil
	if err := d.persistRoots(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	d.swapRoots(legacy)
	calls := make(map[string]int)
	client.list = func(_ context.Context, _, id string, _, _ int) (pan.FilePage, error) {
		calls[id]++
		if id == "10" {
			return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: "parent", Name: "Parent"}, {ID: "10", Name: "Movies10"}}}, nil
		}
		return pan.FilePage{Path: []pan.Directory{{ID: "0"}, {ID: id, Name: id}}}, nil
	}
	if _, err := d.SelectDirectory(ctx, "parent"); !domain.IsKind(err, domain.KindConflict) {
		t.Fatalf("legacy root allowed an overlapping ancestor: %v", err)
	}
	if calls["parent"] != 1 || calls["10"] != 1 || len(d.Sources()) != 1 {
		t.Fatalf("legacy fallback calls=%v mounts=%+v", calls, d.Sources())
	}
	if _, err := d.SelectDirectory(ctx, "20"); err != nil {
		t.Fatal(err)
	}
	if calls["20"] != 1 || calls["10"] != 2 {
		t.Fatalf("legacy root did not fall back for non-overlap: %v", calls)
	}
}

func TestRemovingARootKeepsSiblingSessionsValid(t *testing.T) {
	d, client := mountFixture(t)
	ctx := t.Context()
	selectTestDirectory(t, d, client, testSource.Directory)
	selectTestDirectory(t, d, client, domain.LibraryDirectory{ID: "20", Name: "Other", Path: "/Other"})
	sibling, err := d.OpenSource(ctx, domain.LibrarySource{AccountID: testSource.AccountID, Directory: domain.LibraryDirectory{ID: "20", Name: "Other", Path: "/Other"}})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := d.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveDirectory(ctx, "10"); err != nil {
		t.Fatal(err)
	}
	if len(d.Sources()) != 1 || d.Sources()[0].Directory.ID != "20" {
		t.Fatalf("sources after removal = %+v", d.Sources())
	}
	// The removed root's session dies; its epoch does not resurrect on remount.
	if _, err := primary.List(ctx, "10", 0); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("removed root session = %v", err)
	}
	if _, err := sibling.List(ctx, "20", 0); err != nil {
		t.Fatalf("sibling root session = %v", err)
	}
	selectTestDirectory(t, d, client, testSource.Directory)
	if _, err := primary.List(ctx, "10", 0); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("remounted root accepted the stale session: %v", err)
	}
	if _, err := d.OpenSource(ctx, testSource); err != nil {
		t.Fatalf("remounted root session = %v", err)
	}
}

func TestMountRollsBackWhenAListenerRejectsIt(t *testing.T) {
	d, client := mountFixture(t)
	ctx := t.Context()
	selectTestDirectory(t, d, client, testSource.Directory)
	before := d.snapshot()
	mounts := recordMounts(d)
	rejected := errors.New("cannot queue scan")
	mounts.reject(rejected)
	if _, err := d.SelectDirectory(ctx, "20"); !errors.Is(err, rejected) {
		t.Fatalf("listener failure=%v", err)
	}
	after := d.snapshot()
	saved, found := savedRoots(t, d)
	if mounts.count() != 1 || len(after.roots) != len(before.roots) || after.authorizationVersion != before.authorizationVersion ||
		!found || len(saved) != 1 || !sameMount(saved[0], before.roots[0]) {
		t.Fatalf("failed mount left partial state: memory=%+v saved=%+v found=%t", after.roots, saved, found)
	}
	mounts.reject(nil)
	if _, err := d.SelectDirectory(ctx, "20"); err != nil || d.Sources()[1].Directory.ID != "20" {
		t.Fatalf("retry after a rejected mount: %v", err)
	}
	if sess, err := d.OpenSource(ctx, domain.LibrarySource{AccountID: testSource.AccountID, Directory: domain.LibraryDirectory{ID: "20"}}); err != nil || sess.Source().Directory.ID != "20" {
		t.Fatalf("session after retry = %v, %v", sess, err)
	}
}

func TestLateMountRequestsCannotCorruptAConcurrentMountSet(t *testing.T) {
	for _, next := range []string{"20", "disconnect"} {
		t.Run(next, func(t *testing.T) {
			d, client := mountFixture(t)
			ctx := t.Context()
			started := make(chan struct{}, 1)
			hold, release := testGate(t)
			var listings atomic.Int32
			client.list = func(_ context.Context, _, id string, _, _ int) (pan.FilePage, error) {
				if listings.Add(1) == 1 {
					started <- struct{}{}
					<-hold
				}
				return directoryPage(domain.LibraryDirectory{ID: id, Name: "Movies" + id, Path: "/Movies" + id}), nil
			}
			mounts := recordMounts(d)
			finished := make(chan error, 1)
			go func() { _, err := d.SelectDirectory(ctx, "20"); finished <- err }()
			await(t, started)
			published := mounts.count()
			if next == "disconnect" {
				if _, err := d.Disconnect(ctx); err != nil {
					t.Fatal(err)
				}
			} else if _, err := d.SelectDirectory(ctx, next); err != nil {
				t.Fatal(err)
			}
			version := d.snapshot().authorizationVersion
			release()
			err := await(t, finished)
			switch next {
			case "20":
				// The stale request re-reads the newer state; mounting the same
				// directory again stays idempotent.
				if err != nil {
					t.Fatalf("concurrent mount retry failed: %v", err)
				}
				if len(d.Sources()) != 1 || d.Sources()[0].Directory.ID != "20" {
					t.Fatalf("concurrent mount produced %+v", d.Sources())
				}
			case "disconnect":
				want := pan.ErrUnauthorized
				if !errors.Is(err, want) {
					t.Fatalf("stale account request=%v", err)
				}
				if len(d.Sources()) != 0 {
					t.Fatalf("sources after logout = %+v", d.Sources())
				}
			}
			if d.snapshot().authorizationVersion < version || mounts.count() < published {
				t.Fatal("late request published fewer events than applied changes")
			}
		})
	}
}

func TestVerifiedAccountKeepsOnlyItsOwnDirectories(t *testing.T) {
	d, client := mountedTestDrive(t)
	ctx := t.Context()
	mounts := recordMounts(d)
	if err := d.discardOtherAccountRoots(ctx, testSource.AccountID); err != nil {
		t.Fatal(err)
	}
	if len(d.Sources()) != 1 || d.Sources()[0].Directory.ID != testSource.Directory.ID || mounts.count() != 0 {
		t.Fatalf("same-account verification touched the mounts: sources=%v calls=%d", d.Sources(), mounts.count())
	}
	client.account = func(context.Context, string) (pan.Account, error) { return pan.Account{ID: "different-account"}, nil }
	d.invalidateAccountCache()
	status, err := d.Account(ctx)
	if err != nil || !status.Connected || status.Directories != nil {
		t.Fatalf("other account status = %+v, %v", status, err)
	}
	if _, found := savedRoots(t, d); found || len(d.Sources()) != 0 || mounts.count() != 1 || mounts.lastEvent().Source.Directory.ID != testSource.Directory.ID {
		t.Fatalf("old account mounts retained: sources=%v calls=%d", d.Sources(), mounts.count())
	}
	if mounts.lastEvent().Change != MountRemoved {
		t.Fatalf("discard event = %+v", mounts.lastEvent())
	}
}

func TestClearingAnUnmountedDriveIsHarmless(t *testing.T) {
	d, _ := mountFixture(t)
	for range 2 {
		if err := d.ClearDirectory(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.Sources()) != 0 {
		t.Fatalf("sources after clearing = %+v", d.Sources())
	}
	if _, found := savedRoots(t, d); found {
		t.Fatal("a directory setting appeared while clearing")
	}
}

func TestSessionsTrackTheMountTheyWereIssuedAgainst(t *testing.T) {
	d, client := mountedTestDrive(t)
	ctx := t.Context()
	sess, err := d.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Source() != testSource {
		t.Fatalf("session source = %+v", sess.Source())
	}
	if _, err := d.OpenSource(ctx, domain.LibrarySource{AccountID: testSource.AccountID, Directory: domain.LibraryDirectory{ID: "other"}}); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("session for another source = %v", err)
	}
	client.list = func(_ context.Context, _, id string, _, _ int) (pan.FilePage, error) {
		return pan.FilePage{Total: 1, Files: []pan.File{{ID: "f1"}}, Path: []pan.Directory{{ID: "0"}, {ID: id}}}, nil
	}
	if page, err := sess.List(ctx, testSource.Directory.ID, 0); err != nil || len(page.Files) != 1 {
		t.Fatalf("list = %+v, %v", page, err)
	}
	if info, err := sess.Info(ctx, "f1"); err != nil || info.ID != "f1" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if body, err := sess.Read(ctx, "pick-f1", 1024); err != nil || len(body) == 0 {
		t.Fatalf("read = %q, %v", body, err)
	}
	committed := false
	if err := sess.Commit(ctx, func(*ent.Tx) error { committed = true; return nil }); err != nil || !committed {
		t.Fatalf("commit ran=%t err=%v", committed, err)
	}
	selectTestDirectory(t, d, client, domain.LibraryDirectory{ID: "20", Name: "Other", Path: "/Other"})
	if err := d.RemoveDirectory(ctx, testSource.Directory.ID); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"list":   func() error { _, err := sess.List(ctx, testSource.Directory.ID, 0); return err },
		"info":   func() error { _, err := sess.Info(ctx, "f1"); return err },
		"read":   func() error { _, err := sess.Read(ctx, "pick-f1", 1024); return err },
		"play":   func() error { _, err := sess.PlayURL(ctx, "pick-f1", ""); return err },
		"commit": func() error { return sess.Commit(ctx, func(*ent.Tx) error { return nil }) },
		"guard": func() error {
			return sess.WithSource(ctx, func() error { t.Error("stale source callback ran"); return nil })
		},
	} {
		if err := call(); !errors.Is(err, ErrSourceChanged) {
			t.Fatalf("%s on a removed mount = %v", name, err)
		}
	}
	// Mounting the same directory again must not resurrect the stale session.
	selectTestDirectory(t, d, client, testSource.Directory)
	if _, err := d.OpenSource(ctx, testSource); err != nil {
		t.Fatalf("session after remount = %v", err)
	}
	if _, err := sess.List(ctx, testSource.Directory.ID, 0); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("remount resurrected the stale session: %v", err)
	}
	// Account-scoped work outlives the mount but not the credentials.
	if err := sess.CommitAccount(ctx, func(*ent.Tx) error { return nil }); err != nil {
		t.Fatalf("account commit after remount = %v", err)
	}
	if _, err := d.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sess.CommitAccount(ctx, func(*ent.Tx) error { return nil }); !errors.Is(err, pan.ErrUnauthorized) {
		t.Fatalf("account commit after logout = %v", err)
	}
}

func TestSessionRejectsReadResultsAfterSourceChanges(t *testing.T) {
	for _, action := range []string{"directory", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			d, client := mountedTestDrive(t)
			ctx := t.Context()
			sess, err := d.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			client.list = func(context.Context, string, string, int, int) (pan.FilePage, error) {
				if action == "disconnect" {
					_, err = d.Disconnect(ctx)
				} else {
					err = d.ClearDirectory(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
				return pan.FilePage{Files: []pan.File{{ID: "old-source-file"}}, Total: 1}, nil
			}
			page, err := sess.List(ctx, testSource.Directory.ID, 0)
			if !errors.Is(err, ErrSourceChanged) || len(page.Files) != 0 || page.Total != 0 {
				t.Fatalf("old source response escaped validation: %+v, %v", page, err)
			}
		})
	}
}

func TestExpiredSessionStopsBeforeRefreshingTokens(t *testing.T) {
	d, client := mountedTestDrive(t)
	ctx := t.Context()
	sess, err := d.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ClearDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	expireTokens(d)
	var refreshes atomic.Int32
	client.refreshToken = func(context.Context, string) (pan.Tokens, error) {
		refreshes.Add(1)
		return testTokens("unexpected"), nil
	}
	_, err = sess.List(ctx, testSource.Directory.ID, 0)
	d.Close()
	if !errors.Is(err, ErrSourceChanged) || refreshes.Load() != 0 {
		t.Fatalf("invalid session started a refresh: refreshes=%d err=%v", refreshes.Load(), err)
	}
}

func TestOpenRequiresAMountedDirectoryOfTheVerifiedAccount(t *testing.T) {
	d, client := mountFixture(t)
	ctx := t.Context()
	if _, err := d.Open(ctx); !errors.Is(err, ErrMediaDirectoryRequired) {
		t.Fatalf("open without a mount = %v", err)
	}
	selectTestDirectory(t, d, client, testSource.Directory)
	if _, err := d.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Open(ctx); !errors.Is(err, pan.ErrUnauthorized) {
		t.Fatalf("open after logout = %v", err)
	}
}

func TestWithinRoots(t *testing.T) {
	directories := []domain.LibraryDirectory{testSource.Directory, {ID: "30", Name: "Extras", Path: "/Extras"}}
	for name, test := range map[string]struct {
		info pan.FileInfo
		want bool
	}{
		"the root itself": {pan.FileInfo{File: pan.File{ID: testSource.Directory.ID}}, true},
		"a descendant":    {pan.FileInfo{File: pan.File{ID: "200"}, Path: []pan.Directory{{ID: "0"}, {ID: testSource.Directory.ID}, {ID: "150"}}}, true},
		"a sibling root":  {pan.FileInfo{File: pan.File{ID: "300"}, Path: []pan.Directory{{ID: "0"}, {ID: "30"}, {ID: "310"}}}, true},
		"outside":         {pan.FileInfo{File: pan.File{ID: "400"}, Path: []pan.Directory{{ID: "0"}, {ID: "999"}}}, false},
		"another file":    {pan.FileInfo{File: pan.File{ID: "301"}, Path: []pan.Directory{{ID: "0"}, {ID: "31"}}}, false},
	} {
		if got := WithinRoots(test.info, directories); got != test.want {
			t.Fatalf("%s: within=%t, want %t", name, got, test.want)
		}
	}
	if !WithinRoots(pan.FileInfo{File: pan.File{ID: "300"}}, []domain.LibraryDirectory{{ID: "0"}}) {
		t.Fatal("the 115 root should contain everything")
	}
}

func TestDirectoryPath(t *testing.T) {
	for name, tt := range map[string]struct {
		segments []pan.Directory
		want     string
	}{
		"empty": {segments: nil, want: "/"},
		"root only": {
			segments: []pan.Directory{{ID: "0", Name: "根目录"}},
			want:     "/",
		},
		"single level": {
			segments: []pan.Directory{{ID: "0", Name: "根目录"}, {ID: "1", Name: "Movies"}},
			want:     "/Movies",
		},
		"multi level": {
			segments: []pan.Directory{{ID: "0", Name: "根目录"}, {ID: "1", Name: "Media"}, {ID: "2", Name: "Jav"}},
			want:     "/Media/Jav",
		},
	} {
		if got := directoryPath(tt.segments); got != tt.want {
			t.Errorf("%s: directoryPath() = %q, want %q", name, got, tt.want)
		}
	}
}

func TestFilePath(t *testing.T) {
	segments := []pan.Directory{{ID: "0", Name: "根目录"}, {ID: "1", Name: "Movies"}}
	if got := FilePath(segments, "SSIS-589.mp4"); got != "/Movies/SSIS-589.mp4" {
		t.Errorf("FilePath() = %q, want %q", got, "/Movies/SSIS-589.mp4")
	}
	if got := FilePath(nil, "root.mp4"); got != "/root.mp4" {
		t.Errorf("FilePath() = %q, want %q", got, "/root.mp4")
	}
}

type fakeSessionStub struct {
	Session
	roots []domain.LibraryDirectory
	info  func(ctx context.Context, fileID string) (pan.FileInfo, error)
	list  func(ctx context.Context, dirID string, offset int) (pan.FilePage, error)
}

func (f *fakeSessionStub) Roots() []domain.LibraryDirectory { return f.roots }
func (f *fakeSessionStub) Contains(info pan.FileInfo) bool {
	return WithinRoots(info, f.roots)
}
func (f *fakeSessionStub) Info(ctx context.Context, id string) (pan.FileInfo, error) {
	if f.info != nil {
		return f.info(ctx, id)
	}
	return pan.FileInfo{}, nil
}
func (f *fakeSessionStub) List(ctx context.Context, dirID string, offset int) (pan.FilePage, error) {
	if f.list != nil {
		return f.list(ctx, dirID, offset)
	}
	return pan.FilePage{}, nil
}

func TestSourceInfo(t *testing.T) {
	source := domain.LibrarySource{
		AccountID: "acc-1",
		Directory: domain.LibraryDirectory{ID: "dir-100"},
	}
	sess := &fakeSessionStub{
		roots: []domain.LibraryDirectory{source.Directory},
		info: func(ctx context.Context, fileID string) (pan.FileInfo, error) {
			if fileID == "valid" {
				return pan.FileInfo{
					File: pan.File{ID: "valid", Name: "ABP-001.mp4"},
					Path: []pan.Directory{{ID: "0"}, {ID: "dir-100"}},
				}, nil
			}
			return pan.FileInfo{
				File: pan.File{ID: "outside", Name: "other.mp4"},
				Path: []pan.Directory{{ID: "0"}, {ID: "other-dir"}},
			}, nil
		},
	}

	info, err := SourceInfo(t.Context(), sess, "valid")
	if err != nil || info.Name != "ABP-001.mp4" {
		t.Fatalf("expected valid info, got err=%v, info=%+v", err, info)
	}

	_, err = SourceInfo(t.Context(), sess, "outside")
	if err == nil {
		t.Fatal("expected error for file outside library source, got nil")
	}
}

func TestDirectoryEntries(t *testing.T) {
	source := domain.LibrarySource{
		AccountID: "acc-1",
		Directory: domain.LibraryDirectory{ID: "dir-100"},
	}
	sess := &fakeSessionStub{
		roots: []domain.LibraryDirectory{source.Directory},
		list: func(ctx context.Context, dirID string, offset int) (pan.FilePage, error) {
			if dirID == "dir-inside" {
				return pan.FilePage{
					Total: 2,
					Path:  []pan.Directory{{ID: "0"}, {ID: "dir-100"}, {ID: "dir-inside"}},
					Files: []pan.File{{ID: "f1", Name: "a.mp4"}, {ID: "f2", Name: "b.mp4"}},
				}, nil
			}
			return pan.FilePage{
				Total: 1,
				Path:  []pan.Directory{{ID: "0"}, {ID: "other"}},
				Files: []pan.File{{ID: "f3", Name: "c.mp4"}},
			}, nil
		},
	}

	files, err := DirectoryEntries(t.Context(), sess, "dir-inside")
	if err != nil || len(files) != 2 {
		t.Fatalf("expected 2 files, got err=%v, files=%+v", err, files)
	}

	_, err = DirectoryEntries(t.Context(), sess, "dir-outside")
	if err == nil {
		t.Fatal("expected error for directory outside library source, got nil")
	}
}
