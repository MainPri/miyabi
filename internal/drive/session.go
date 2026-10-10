package drive

import (
	"context"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/pan"
)

// Session represents controlled access to 115 during an operation. It is
// pinned to one mounted root (the primary root for unpinned opens), carries
// that root's epoch and the credential version at issue, checks source-bound
// reads before and after each request, and serializes database commits under
// the drive commit lock. Containment checks accept any mounted root so files
// of sibling roots stay reachable.
type Session interface {
	Source() domain.LibrarySource
	// Roots returns the currently mounted directories of the account.
	Roots() []domain.LibraryDirectory
	// AuthorizationVersion changes on login, disconnect, and mount changes.
	// It scopes derived data without exposing credentials or expiring on token refresh.
	AuthorizationVersion() uint64
	List(ctx context.Context, dirID string, offset int) (pan.FilePage, error)
	Info(ctx context.Context, fileID string) (pan.FileInfo, error)
	Read(ctx context.Context, pickCode string, limit int64) ([]byte, error)
	WithSource(ctx context.Context, fn func() error) error
	Commit(ctx context.Context, fn func(tx *ent.Tx) error) error
	CommitAccount(ctx context.Context, fn func(tx *ent.Tx) error) error
	// Contains reports whether a file info record belongs inside any mounted root.
	Contains(info pan.FileInfo) bool

	PlayURL(ctx context.Context, pickCode, userAgent string) ([]pan.PlaySource, error)
	DownloadURL(ctx context.Context, pickCode, userAgent string) (string, error)
	AddOffline(ctx context.Context, magnet string) (string, error)
	RemoveOffline(ctx context.Context, hash string) error
	OfflineTasks(ctx context.Context, page int) (pan.OfflinePage, error)
}

// Open verifies the current mount and authenticated account, returning a
// Session pinned to the account's primary root.
func (d *Drive) Open(ctx context.Context) (Session, error) {
	state, record, err := d.verifiedSource(ctx)
	if err != nil {
		return nil, err
	}
	return newSession(d, state, record.source()), nil
}

// OpenSource opens a Session ensuring the expected root is still mounted.
func (d *Drive) OpenSource(ctx context.Context, expected domain.LibrarySource) (Session, error) {
	state := d.snapshot()
	account, err := d.verifyAccount(ctx, state)
	if err != nil {
		return nil, err
	}
	record, mounted := state.root(expected.Directory.ID)
	if !mounted || record.AccountID != account.ID || record.AccountID != expected.AccountID {
		return nil, ErrSourceChanged
	}
	return newSession(d, state, expected), nil
}

func newSession(d *Drive, state snapshot, source domain.LibrarySource) Session {
	return &sourceSession{
		drive:             d,
		source:            source,
		epoch:             state.epochs[source.Directory.ID],
		version:           state.authorizationVersion,
		credentialVersion: state.credentialVersion,
	}
}

type sourceSession struct {
	drive             *Drive
	source            domain.LibrarySource
	epoch             uint64
	version           uint64
	credentialVersion uint64
}

func (s *sourceSession) Source() domain.LibrarySource {
	return s.source
}

func (s *sourceSession) Roots() []domain.LibraryDirectory {
	state, err := s.drive.sourceState(s.source, s.epoch, s.credentialVersion)
	if err != nil {
		return nil
	}
	directories := make([]domain.LibraryDirectory, 0, len(state.roots))
	for _, record := range state.sources(s.source.AccountID) {
		directories = append(directories, record.Directory)
	}
	return directories
}

func (s *sourceSession) AuthorizationVersion() uint64 {
	return s.version
}

func (s *sourceSession) checkSource() error {
	_, err := s.drive.sourceState(s.source, s.epoch, s.credentialVersion)
	return err
}

// Contains validates against the live root set so sibling roots count too.
func (s *sourceSession) Contains(info pan.FileInfo) bool {
	roots := s.Roots()
	if roots == nil {
		return false
	}
	return WithinRoots(info, roots)
}

// readSource discards read results from a replaced source. Remote mutations
// keep their own completion semantics and must not use this helper.
func readSource[T any](ctx context.Context, s *sourceSession, request func(string) (T, error)) (T, error) {
	var zero T
	state, err := s.drive.sourceState(s.source, s.epoch, s.credentialVersion)
	if err != nil {
		return zero, err
	}
	value, err := withPanSourceToken(ctx, s.drive, state, s, request)
	if err != nil {
		return zero, err
	}
	if err := s.checkSource(); err != nil {
		return zero, err
	}
	return value, nil
}

func (s *sourceSession) List(ctx context.Context, dirID string, offset int) (pan.FilePage, error) {
	return readSource(ctx, s, func(token string) (pan.FilePage, error) {
		return s.drive.client.List(ctx, token, dirID, offset, 100)
	})
}

func (s *sourceSession) Info(ctx context.Context, fileID string) (pan.FileInfo, error) {
	return readSource(ctx, s, func(token string) (pan.FileInfo, error) {
		return s.drive.client.Info(ctx, token, fileID)
	})
}

func (s *sourceSession) Read(ctx context.Context, pickCode string, limit int64) ([]byte, error) {
	return readSource(ctx, s, func(token string) ([]byte, error) {
		return s.drive.client.ReadMetadata(ctx, token, pickCode, limit)
	})
}

func (s *sourceSession) PlayURL(ctx context.Context, pickCode, userAgent string) ([]pan.PlaySource, error) {
	return readSource(ctx, s, func(token string) ([]pan.PlaySource, error) {
		return s.drive.client.PlayURL(ctx, token, pickCode, userAgent)
	})
}

func (s *sourceSession) DownloadURL(ctx context.Context, pickCode, userAgent string) (string, error) {
	return readSource(ctx, s, func(token string) (string, error) {
		return s.drive.client.DownloadURL(ctx, token, pickCode, userAgent)
	})
}

func (s *sourceSession) AddOffline(ctx context.Context, magnet string) (string, error) {
	state, err := s.drive.sourceState(s.source, s.epoch, s.credentialVersion)
	if err != nil {
		return "", err
	}
	return withPanSourceToken(ctx, s.drive, state, s, func(token string) (string, error) {
		return s.drive.client.AddOffline(ctx, token, magnet, s.source.Directory.ID)
	})
}

func (s *sourceSession) RemoveOffline(ctx context.Context, hash string) error {
	state, err := s.drive.sourceState(s.source, s.epoch, s.credentialVersion)
	if err != nil {
		return err
	}
	_, err = withPanSourceToken(ctx, s.drive, state, s, func(token string) (struct{}, error) {
		return struct{}{}, s.drive.client.RemoveOffline(ctx, token, hash)
	})
	if err != nil {
		return err
	}
	return s.checkSource()
}

func (s *sourceSession) OfflineTasks(ctx context.Context, page int) (pan.OfflinePage, error) {
	state := snapshot{credentialVersion: s.credentialVersion}
	return withPanToken(ctx, s.drive, state, func(current snapshot) (pan.OfflinePage, error) {
		return s.drive.client.OfflineTasks(ctx, current.tokens.AccessToken, page)
	})
}

func (s *sourceSession) Commit(ctx context.Context, fn func(tx *ent.Tx) error) error {
	return s.WithSource(ctx, func() error { return ent.WithTx(ctx, s.drive.database, fn) })
}

// WithSource excludes source changes without opening a database transaction.
// The callback must not reenter session commits or change the active source.
func (s *sourceSession) WithSource(ctx context.Context, fn func() error) error {
	if err := s.drive.commit.Lock(ctx); err != nil {
		return err
	}
	defer s.drive.commit.Unlock()
	if _, err := s.drive.sourceState(s.source, s.epoch, s.credentialVersion); err != nil {
		return err
	}
	return fn()
}

func (s *sourceSession) CommitAccount(ctx context.Context, fn func(tx *ent.Tx) error) error {
	if err := s.drive.commit.Lock(ctx); err != nil {
		return err
	}
	defer s.drive.commit.Unlock()
	current, err := s.drive.credentials(snapshot{credentialVersion: s.credentialVersion})
	if err != nil {
		return err
	}
	if current.closed {
		return context.Canceled
	}
	return ent.WithTx(ctx, s.drive.database, fn)
}

// SourceInfo fetches file info and validates that it resides within the session's mounted roots.
func SourceInfo(ctx context.Context, sess Session, id string) (pan.FileInfo, error) {
	info, err := sess.Info(ctx, id)
	if err != nil {
		return pan.FileInfo{}, err
	}
	if !sess.Contains(info) {
		return pan.FileInfo{}, domain.E(domain.KindNotFound, "下载资源已移出媒体目录", nil)
	}
	return info, nil
}

// DirectoryEntries lists all file entries across pages in a directory, validating that the directory is within the mounted roots.
func DirectoryEntries(ctx context.Context, sess Session, id string) ([]pan.File, error) {
	var files []pan.File
	err := WalkFilePages(ctx, func(offset int) (pan.FilePage, error) {
		page, err := sess.List(ctx, id, offset)
		if err != nil {
			return pan.FilePage{}, err
		}
		if !sess.Contains(pan.FileInfo{File: pan.File{ID: id}, Path: page.Path}) {
			return pan.FilePage{}, domain.E(domain.KindConflict, "该文件夹已移出媒体目录，请重新扫描", nil)
		}
		return page, nil
	}, func(page pan.FilePage) (bool, error) {
		files = append(files, page.Files...)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
