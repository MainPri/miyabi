package drive

import (
	"context"

	"github.com/ppxb/miyabi/internal/domain"
)

// MountChange describes what happened to the mounted directory set.
type MountChange int

const (
	// MountAdded signals a directory was mounted; the listener queues its first scan.
	MountAdded MountChange = iota
	// MountRemoved signals one directory was unmounted; the listener prunes its index.
	MountRemoved
)

// MountEvent signals that a media directory has been mounted or unmounted.
type MountEvent struct {
	Change MountChange
	Source domain.LibrarySource
}

// MountListener receives mount notifications.
type MountListener func(ctx context.Context, event MountEvent) error

// SetMountListener replaces the synchronous mount callback; nil clears it.
// The callback runs under the commit lock, but not the state lock. It must not
// open sessions or commit through Drive. A rejected mount restores its old
// state; a failed removal keeps the unmount and reports the listener error.
func (d *Drive) SetMountListener(listener MountListener) {
	d.mu.Lock()
	d.mountListener = listener
	d.mu.Unlock()
}

func (d *Drive) publishMount(ctx context.Context, event MountEvent) error {
	d.mu.Lock()
	listener := d.mountListener
	d.mu.Unlock()
	if listener == nil {
		return nil
	}
	return listener(ctx, event)
}
