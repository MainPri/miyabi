package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/library/scrape"
)

// PruneSource drops the index and exported attachments of an unmounted
// source. Movies with references in other mounted roots survive.
func PruneSource(ctx context.Context, db *ent.Client, source domain.LibrarySource, cfg export.Config, notifier MediaNotifier) error {
	var removed []string
	if err := ent.WithTx(ctx, db, func(tx *ent.Tx) error {
		stale := database.LibraryFiles(source)
		movies, err := tx.File.Query().Where(stale).QueryMovie().IDs(ctx)
		if err != nil {
			return fmt.Errorf("find pruned movie files: %w", err)
		}
		if _, err := tx.File.Delete().Where(stale).Exec(ctx); err != nil {
			return fmt.Errorf("remove unmounted file indexes: %w", err)
		}
		removed, err = RemoveUnreferencedMovies(ctx, tx, movies)
		return err
	}); err != nil {
		return err
	}
	if cfg.EmbyDir == "" || len(removed) == 0 {
		return nil
	}
	var cleanupErrors []error
	for _, code := range removed {
		movieDir := scrape.EmbyMovieDir(cfg.EmbyDir, code)
		if err := os.RemoveAll(movieDir); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove exported movie %s: %w", code, err))
			continue
		}
		_ = os.Remove(filepath.Dir(movieDir)) // Keep non-empty prefix directories.
		if notifier != nil {
			cleanupErrors = append(cleanupErrors, notifier.NotifyUpdated(context.WithoutCancel(ctx), movieDir))
		}
	}
	return errors.Join(cleanupErrors...)
}
