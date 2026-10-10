package drive

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/setting"
	"github.com/ppxb/miyabi/internal/pan"
)

// Files retrieves a single page of directory items from 115 for folder navigation.
func (d *Drive) Files(ctx context.Context, directoryID string, page int) (pan.FilePage, error) {
	state := d.snapshot()
	files, err := withPanToken(ctx, d, state, func(current snapshot) (pan.FilePage, error) {
		return d.client.List(ctx, current.tokens.AccessToken, directoryID, (page-1)*100, 100)
	})
	if err != nil {
		return pan.FilePage{}, fmt.Errorf("list 115 directory: %w", err)
	}
	if _, err := d.credentials(state); err != nil {
		return pan.FilePage{}, err
	}
	return files, nil
}

// SelectDirectory mounts the given 115 directory as an additional scan root,
// saves it, updates the in-memory state, and emits a MountChanged event.
// Directories containing or contained by an existing root are rejected so
// every indexed file has exactly one owning root.
func (d *Drive) SelectDirectory(ctx context.Context, directoryID string) (domain.LibraryDirectory, error) {
	for range 5 {
		state := d.snapshot()
		account, err := d.verifyAccount(ctx, state)
		if err != nil {
			return domain.LibraryDirectory{}, fmt.Errorf("get 115 account for directory: %w", err)
		}
		files, err := withPanToken(ctx, d, state, func(current snapshot) (pan.FilePage, error) {
			return d.client.List(ctx, current.tokens.AccessToken, directoryID, 0, 1)
		})
		if err != nil {
			return domain.LibraryDirectory{}, fmt.Errorf("get 115 media directory: %w", err)
		}
		if len(files.Path) == 0 {
			return domain.LibraryDirectory{}, domain.E(domain.KindNotFound, "115 目录不存在或已被移除", nil)
		}
		directory := domain.LibraryDirectory{
			ID: directoryID, Name: files.Path[len(files.Path)-1].Name,
			Path: directoryPath(files.Path),
		}
		record := mountRecord{AccountID: account.ID, LibraryDirectory: directory}
		if err := d.checkRootOverlap(ctx, state, account.ID, record, files.Path); err != nil {
			return domain.LibraryDirectory{}, err
		}

		if err := d.commit.Lock(ctx); err != nil {
			return domain.LibraryDirectory{}, err
		}
		if d.snapshot().authorizationVersion != state.authorizationVersion {
			d.commit.Unlock()
			continue
		}
		current, err := d.credentials(state)
		if err != nil {
			d.commit.Unlock()
			return domain.LibraryDirectory{}, err
		}
		if previous, mounted := current.root(record.ID); mounted {
			d.commit.Unlock()
			if previous.AccountID == record.AccountID {
				return previous.LibraryDirectory, nil
			}
			// A stale record of another account: replace it like a fresh mount.
			if err := d.replaceRoot(ctx, previous, record); err != nil {
				return domain.LibraryDirectory{}, err
			}
			return directory, nil
		}
		if err := d.addRoot(ctx, record); err != nil {
			d.commit.Unlock()
			return domain.LibraryDirectory{}, err
		}
		d.commit.Unlock()
		return directory, nil
	}
	return domain.LibraryDirectory{}, domain.E(domain.KindConflict, "媒体目录正在变化，请稍后重试", nil)
}

// checkRootOverlap rejects nesting a new root inside an existing one or the
// other way around, using each root's 115 ancestry chain.
func (d *Drive) checkRootOverlap(ctx context.Context, state snapshot, accountID string, record mountRecord, chain []pan.Directory) error {
	for _, existing := range state.sources(accountID) {
		if existing.Directory.ID == record.ID {
			continue // Re-selecting the same directory stays idempotent.
		}
		if existing.Directory.ID == "0" || record.ID == "0" {
			return domain.E(domain.KindConflict, "媒体目录不能与其他挂载目录重叠", nil)
		}
		if slices.ContainsFunc(chain, func(dir pan.Directory) bool { return dir.ID == existing.Directory.ID }) {
			return domain.E(domain.KindConflict, fmt.Sprintf("该目录位于已挂载的 %s 之内，请直接挂载其上层目录", existing.Directory.Path), nil)
		}
		files, err := withPanToken(ctx, d, state, func(current snapshot) (pan.FilePage, error) {
			return d.client.List(ctx, current.tokens.AccessToken, existing.Directory.ID, 0, 1)
		})
		if err != nil {
			return fmt.Errorf("check mounted directory %s: %w", existing.Directory.Path, err)
		}
		if slices.ContainsFunc(files.Path, func(dir pan.Directory) bool { return dir.ID == record.ID }) {
			return domain.E(domain.KindConflict, fmt.Sprintf("该目录已包含已挂载的 %s，请改为挂载其子目录", existing.Directory.Path), nil)
		}
	}
	return nil
}

// addRoot requires the commit lock and a verified unchanged snapshot. The
// mount is complete only once the listener has accepted it; the library queues
// the scan here. On failure nothing observable remains.
func (d *Drive) addRoot(ctx context.Context, record mountRecord) error {
	previous := d.snapshot()
	roots := append(slices.Clone(previous.roots), record)
	if err := d.persistRoots(ctx, roots); err != nil {
		return err
	}
	d.swapRoots(roots)
	if err := d.publishMount(ctx, MountEvent{Change: MountAdded, Source: record.source()}); err != nil {
		d.restore(previous)
		if rollback := d.persistRoots(ctx, previous.roots); rollback != nil {
			return fmt.Errorf("mount media directory: %w (rollback failed: %v)", err, rollback)
		}
		return fmt.Errorf("mount media directory: %w", err)
	}
	return nil
}

// replaceRoot re-mounts a directory record under another account without a
// listener round trip: the mount itself does not change.
func (d *Drive) replaceRoot(ctx context.Context, previous, record mountRecord) error {
	state := d.snapshot()
	roots := slices.Clone(state.roots)
	for i, entry := range roots {
		if entry.ID == previous.ID {
			roots[i] = record
		}
	}
	if err := d.persistRoots(ctx, roots); err != nil {
		return err
	}
	d.swapRoots(roots)
	return nil
}

// RemoveDirectory unmounts one media directory and emits a MountChanged
// event; the listener prunes its index and exports.
func (d *Drive) RemoveDirectory(ctx context.Context, directoryID string) error {
	if err := d.commit.Lock(ctx); err != nil {
		return err
	}
	defer d.commit.Unlock()
	state := d.snapshot()
	record, mounted := state.root(directoryID)
	if !mounted {
		return domain.E(domain.KindNotFound, "该目录未挂载", nil)
	}
	remaining := slices.DeleteFunc(slices.Clone(state.roots), func(entry mountRecord) bool { return entry.ID == directoryID })
	if err := d.persistRoots(ctx, remaining); err != nil {
		return err
	}
	d.swapRoots(remaining)
	if err := d.publishMount(ctx, MountEvent{Change: MountRemoved, Source: record.source()}); err != nil {
		return fmt.Errorf("unmount media directory: %w", err)
	}
	return nil
}

// ClearDirectory unmounts every media directory and emits a MountChanged event per root.
func (d *Drive) ClearDirectory(ctx context.Context) error {
	if err := d.commit.Lock(ctx); err != nil {
		return err
	}
	defer d.commit.Unlock()
	for {
		state := d.snapshot()
		if len(state.roots) == 0 {
			return nil
		}
		if err := d.removeRootLocked(ctx, state.roots[len(state.roots)-1]); err != nil {
			return err
		}
	}
}

func (d *Drive) removeRootLocked(ctx context.Context, record mountRecord) error {
	state := d.snapshot()
	remaining := slices.DeleteFunc(slices.Clone(state.roots), func(entry mountRecord) bool { return entry.ID == record.ID })
	if err := d.persistRoots(ctx, remaining); err != nil {
		return err
	}
	d.swapRoots(remaining)
	if err := d.publishMount(ctx, MountEvent{Change: MountRemoved, Source: record.source()}); err != nil {
		return fmt.Errorf("publish unmount event: %w", err)
	}
	return nil
}

// discardOtherAccountRoots drops roots belonging to another account after a
// login or account check, pruning each through the mount listener.
func (d *Drive) discardOtherAccountRoots(ctx context.Context, accountID string) error {
	for _, source := range d.snapshot().sources("") {
		if source.AccountID == accountID {
			continue
		}
		record, mounted := d.snapshot().root(source.Directory.ID)
		if !mounted {
			continue
		}
		if err := d.removeRootLocked(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

// swapRoots publishes a new mount set in memory and returns what it replaced.
// Epochs advance only for roots whose record changed; siblings of a mount
// change keep their epochs so their sessions survive. Epochs of removed roots
// stay behind as tombstones so remounting cannot resurrect stale sessions.
func (d *Drive) swapRoots(roots []mountRecord) snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	previous := snapshot{
		tokens:               d.tokens,
		roots:                d.roots,
		epochs:               d.epochs,
		authorizationVersion: d.authorizationVersion,
		credentialVersion:    d.credentialVersion,
		tokenVersion:         d.tokenVersion,
	}
	next := make(map[string]uint64, len(previous.epochs)+1)
	maps.Copy(next, previous.epochs)
	for _, record := range roots {
		next[record.ID] = previous.epochs[record.ID] + 1
		for _, old := range previous.roots {
			if old.ID == record.ID && old == record {
				next[record.ID] = previous.epochs[record.ID]
				break
			}
		}
	}
	d.roots = roots
	d.epochs = next
	d.authorizationVersion++
	return previous
}

func (d *Drive) restore(previous snapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.roots = previous.roots
	d.epochs = previous.epochs
	d.authorizationVersion = previous.authorizationVersion
}

// persistRoots stores the mounted directories, or removes the setting when empty.
func (d *Drive) persistRoots(ctx context.Context, roots []mountRecord) error {
	if len(roots) == 0 {
		if _, err := d.database.Setting.Delete().Where(setting.Key(directoriesSetting)).Exec(ctx); err != nil {
			return fmt.Errorf("remove 115 media directories setting: %w", err)
		}
		return nil
	}
	if err := database.SaveSetting(ctx, d.database, directoriesSetting, roots); err != nil {
		return fmt.Errorf("save media directories setting: %w", err)
	}
	return nil
}

// WithinRoots checks whether a file info record belongs inside any of the mounted roots.
func WithinRoots(info pan.FileInfo, directories []domain.LibraryDirectory) bool {
	if slices.ContainsFunc(directories, func(dir domain.LibraryDirectory) bool {
		return info.ID == dir.ID || dir.ID == "0"
	}) {
		return true
	}
	return slices.ContainsFunc(info.Path, func(dir pan.Directory) bool {
		return slices.ContainsFunc(directories, func(mounted domain.LibraryDirectory) bool { return mounted.ID == dir.ID })
	})
}

// directoryPath builds a root-anchored path string from 115 path segments,
// skipping the root folder (ID "0").
func directoryPath(segments []pan.Directory) string {
	names := make([]string, 0, len(segments))
	for _, directory := range segments {
		if directory.ID != "0" && directory.Name != "" {
			names = append(names, directory.Name)
		}
	}
	return "/" + strings.Join(names, "/")
}

// FilePath builds a root-anchored path string from 115 path segments and a file name.
func FilePath(segments []pan.Directory, name string) string {
	dir := directoryPath(segments)
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}
