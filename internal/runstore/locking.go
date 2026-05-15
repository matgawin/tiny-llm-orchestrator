package runstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

var (
	runLocks              sync.Map
	runLockWaitObserverMu sync.Mutex
	runLockWaitObserver   func(string)
)

type contextRunLock struct {
	ch chan struct{}
}

func newContextRunLock() *contextRunLock {
	return &contextRunLock{ch: make(chan struct{}, 1)}
}

func (l *contextRunLock) lock(ctx context.Context, lockName string) error {
	select {
	case l.ch <- struct{}{}:
		return nil
	default:
		observeRunLockWait(lockName)
		select {
		case l.ch <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *contextRunLock) unlock() {
	select {
	case <-l.ch:
	default:
	}
}

// SetRunLockWaitObserverForTest installs a test observer called when a run-lock
// wait path observes contention. It returns a cleanup function.
func SetRunLockWaitObserverForTest(observer func(string)) func() {
	runLockWaitObserverMu.Lock()
	previous := runLockWaitObserver
	runLockWaitObserver = observer
	runLockWaitObserverMu.Unlock()
	return func() {
		runLockWaitObserverMu.Lock()
		runLockWaitObserver = previous
		runLockWaitObserverMu.Unlock()
	}
}

func observeRunLockWait(lockName string) {
	runLockWaitObserverMu.Lock()
	observer := runLockWaitObserver
	runLockWaitObserverMu.Unlock()
	if observer != nil {
		observer(lockName)
	}
}

func (s *Store) lockRunsDir(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, stableerr.New("context is required")
	}
	if err := validateRunsDir(s.orcDir, s.runsDir); err != nil {
		return nil, fmt.Errorf("runs directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(s.runsDir, ".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to the validated runs directory.
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd()) // #nosec G115 -- file descriptors fit int on supported Linux targets.
	if err := flockExclusiveContext(ctx, fd, "runs-directory"); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func flockExclusiveContext(ctx context.Context, fd int, lockName string) error {
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		observeRunLockWait(lockName)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *Store) withRunLock(runID string, fn func() error) error {
	return s.withRunLockContext(context.Background(), runID, fn)
}

func (s *Store) withRunLockContext(ctx context.Context, runID string, fn func() error) error {
	if ctx == nil {
		return stableerr.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlockRuns, err := s.lockRunsDir(ctx)
	if err != nil {
		return err
	}
	runsReleased := false
	releaseRuns := func() {
		if !runsReleased {
			unlockRuns()
			runsReleased = true
		}
	}
	defer releaseRuns()
	runDir := s.runDir(runID)
	if err := validateDir(runDir); err != nil {
		return fmt.Errorf("run %q directory: %w", runID, err)
	}
	localLockValue, _ := runLocks.LoadOrStore(runDir, newContextRunLock())
	localLock, ok := localLockValue.(*contextRunLock)
	if !ok {
		return stableerr.Errorf("run %q lock has unexpected type %T", runID, localLockValue)
	}
	if err := localLock.lock(ctx, runID); err != nil {
		return err
	}
	defer localLock.unlock()
	lockPath := filepath.Join(runDir, ".lock")
	if err := validateRunLockFile(lockPath); err != nil {
		return err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to a validated run directory and O_NOFOLLOW rejects symlinks.
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	if info, err := file.Stat(); err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return stableerr.Errorf("run %q lock is not a regular file", runID)
	}
	fd := int(file.Fd()) // #nosec G115 -- file descriptors fit int on supported Linux targets.
	if err := flockExclusiveContext(ctx, fd, runID); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	releaseRuns()
	return fn()
}

func validateRunLockFile(lockPath string) error {
	info, err := os.Lstat(lockPath) // #nosec G703 -- lock path is scoped to a validated run directory.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateFileInfo("run lock", info)
}
