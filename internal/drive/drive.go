package drive

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/ent/setting"
	"github.com/ppxb/miyabi/internal/pan"
	"github.com/ppxb/miyabi/internal/syncx"
	"golang.org/x/sync/singleflight"
)

const (
	credentialsSetting = "pan.credentials"
	directoriesSetting = "pan.library_directories"
	// legacyDirectorySetting holds the pre-multimount single directory record.
	legacyDirectorySetting = "pan.library_directory"
)

// Client abstracts the upstream 115 API calls.
type Client interface {
	Close()
	Account(context.Context, string) (pan.Account, error)
	BeginLogin(context.Context) (*pan.Login, error)
	LoginStatus(context.Context, *pan.Login) (pan.LoginState, error)
	ExchangeToken(context.Context, *pan.Login) (pan.Tokens, error)
	RefreshToken(context.Context, string) (pan.Tokens, error)
	List(context.Context, string, string, int, int) (pan.FilePage, error)
	Info(context.Context, string, string) (pan.FileInfo, error)
	ReadMetadata(context.Context, string, string, int64) ([]byte, error)
	AddOffline(context.Context, string, string, string) (string, error)
	RemoveOffline(context.Context, string, string) error
	OfflineTasks(context.Context, string, int) (pan.OfflinePage, error)
	PlayURL(context.Context, string, string, string) ([]pan.PlaySource, error)
	DownloadURL(context.Context, string, string, string) (string, error)
	OpenMedia(context.Context, string, string, http.Header) (*http.Response, error)
}

type mountRecord struct {
	AccountID string `json:"account_id"`
	domain.LibraryDirectory
}

func (r mountRecord) source() domain.LibrarySource {
	return domain.LibrarySource{AccountID: r.AccountID, Directory: r.LibraryDirectory}
}

type snapshot struct {
	tokens               pan.Tokens
	roots                []mountRecord
	epochs               map[string]uint64
	authorizationVersion uint64
	credentialVersion    uint64
	tokenVersion         uint64
	closed               bool
}

// root returns the mounted record of a directory ID.
func (s snapshot) root(directoryID string) (mountRecord, bool) {
	for _, record := range s.roots {
		if record.ID == directoryID {
			return record, true
		}
	}
	return mountRecord{}, false
}

// matchesSource reports whether a session pinned to the given source and root
// epoch is still valid. Each root carries its own epoch so mounting or removing
// another root does not invalidate this one.
func (s snapshot) matchesSource(source domain.LibrarySource, epoch uint64) bool {
	record, ok := s.root(source.Directory.ID)
	return ok && s.epochs[source.Directory.ID] == epoch && record.AccountID == source.AccountID
}

// primary returns the first root mounted for the account; new offline
// downloads and unpinned sessions target it.
func (s snapshot) primary(accountID string) (mountRecord, bool) {
	for _, record := range s.roots {
		if record.AccountID == accountID {
			return record, true
		}
	}
	return mountRecord{}, false
}

// sources returns every mounted source, optionally narrowed to one account.
func (s snapshot) sources(accountID string) []domain.LibrarySource {
	result := make([]domain.LibrarySource, 0, len(s.roots))
	for _, record := range s.roots {
		if accountID == "" || record.AccountID == accountID {
			result = append(result, record.source())
		}
	}
	return result
}

// Drive coordinates 115 credentials, mounted directories, and authorization lifecycle.
type Drive struct {
	database *ent.Client
	client   Client

	mu                   sync.Mutex
	commit               syncx.ContextLock
	tokens               pan.Tokens
	roots                []mountRecord
	epochs               map[string]uint64
	authorizationVersion uint64
	credentialVersion    uint64
	tokenVersion         uint64
	refresh              singleflight.Group
	work                 sync.WaitGroup
	closed               bool

	accountCacheMu          sync.Mutex
	accountChecks           singleflight.Group
	cachedAccount           pan.Account
	cachedAccountTime       time.Time
	cachedCredentialVersion uint64

	login         qrLogin
	mountListener MountListener
}

func New(ctx context.Context, db *ent.Client) (*Drive, error) {
	return NewWithClient(ctx, db, pan.New())
}

func NewWithClient(ctx context.Context, db *ent.Client, client Client) (*Drive, error) {
	tokens, _, err := database.LoadSetting[pan.Tokens](ctx, db, credentialsSetting)
	if err != nil {
		return nil, err
	}
	roots, err := loadRoots(ctx, db)
	if err != nil {
		return nil, err
	}
	d := &Drive{
		database: db,
		client:   client,
		tokens:   tokens,
		roots:    roots,
		epochs:   make(map[string]uint64, len(roots)),
	}
	for _, record := range roots {
		d.epochs[record.ID] = 1
	}
	d.login.drive = d
	return d, nil
}

// loadRoots reads the mounted directories, migrating the legacy single
// directory setting on first startup after an upgrade.
func loadRoots(ctx context.Context, db *ent.Client) ([]mountRecord, error) {
	roots, found, err := database.LoadSetting[[]mountRecord](ctx, db, directoriesSetting)
	if err != nil {
		return nil, err
	}
	if found {
		return roots, nil
	}
	legacy, found, err := database.LoadSetting[mountRecord](ctx, db, legacyDirectorySetting)
	if err != nil {
		return nil, err
	}
	if !found || legacy.ID == "" {
		return nil, nil
	}
	roots = []mountRecord{legacy}
	if err := database.SaveSetting(ctx, db, directoriesSetting, roots); err != nil {
		return nil, err
	}
	if _, err := db.Setting.Delete().Where(setting.Key(legacyDirectorySetting)).Exec(ctx); err != nil {
		return nil, err
	}
	return roots, nil
}

func (d *Drive) Close() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.work.Wait()
	d.client.Close()
}

func (d *Drive) snapshot() snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return snapshot{
		tokens:               d.tokens,
		roots:                slices.Clone(d.roots),
		epochs:               maps.Clone(d.epochs),
		authorizationVersion: d.authorizationVersion,
		credentialVersion:    d.credentialVersion,
		tokenVersion:         d.tokenVersion,
		closed:               d.closed,
	}
}

func (d *Drive) credentials(expected snapshot) (snapshot, error) {
	current := d.snapshot()
	if current.credentialVersion != expected.credentialVersion || current.tokens.AccessToken == "" {
		return snapshot{}, pan.ErrUnauthorized
	}
	return current, nil
}

func (d *Drive) sourceState(source domain.LibrarySource, epoch, credentialVersion uint64) (snapshot, error) {
	state := d.snapshot()
	if state.credentialVersion != credentialVersion || !state.matchesSource(source, epoch) {
		return snapshot{}, ErrSourceChanged
	}
	return state, nil
}

// verifiedSource checks the account and returns its primary mounted root.
func (d *Drive) verifiedSource(ctx context.Context) (snapshot, mountRecord, error) {
	state := d.snapshot()
	account, err := d.verifyAccount(ctx, state)
	if err != nil {
		return snapshot{}, mountRecord{}, err
	}
	record, ok := state.primary(account.ID)
	if !ok {
		return snapshot{}, mountRecord{}, ErrMediaDirectoryRequired
	}
	return state, record, nil
}

// StartWork registers work that Close must wait for, such as a remote
// mutation that has to be recorded once started. It returns false once the
// drive is closed.
func (d *Drive) StartWork() (func(), bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, false
	}
	d.work.Add(1)
	return d.work.Done, true
}

// Commit executes a database transaction under the drive commit lock.
func (d *Drive) Commit(ctx context.Context, fn func(tx *ent.Tx) error) error {
	if err := d.commit.Lock(ctx); err != nil {
		return err
	}
	defer d.commit.Unlock()
	return ent.WithTx(ctx, d.database, fn)
}

// OpenMedia streams media from 115 CDN directly.
func (d *Drive) OpenMedia(ctx context.Context, method, address string, headers http.Header) (*http.Response, error) {
	return d.client.OpenMedia(ctx, method, address, headers)
}

// Sources returns every currently mounted library source.
func (d *Drive) Sources() []domain.LibrarySource {
	return d.snapshot().sources("")
}
