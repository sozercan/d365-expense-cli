package cli

import (
	"errors"
	"fmt"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/expense"
	sessionstore "github.com/sozercan/d365-expense-cli/internal/session"
)

func validateProfileSource(command, harPath, sessionName string) error {
	if (harPath == "") == (sessionName == "") {
		return fmt.Errorf("%s requires exactly one of --har or --session", command)
	}
	return nil
}

func loadBootstrapForRead(harPath, sessionName string) (*capture.BootstrapProfile, error) {
	if harPath != "" {
		if err := requirePrivateCapture(harPath); err != nil {
			return nil, err
		}
		return capture.LoadBootstrap(harPath)
	}
	store, err := sessionstore.DefaultStore()
	if err != nil {
		return nil, err
	}
	stored, err := store.Load(sessionName)
	if err != nil {
		return nil, err
	}
	if stored.Status != sessionstore.StatusReady {
		return nil, fmt.Errorf("session %q is %s; re-import a fresh HAR", sessionName, stored.Status)
	}
	return stored.Bootstrap()
}

type namedSessionExecution struct {
	name   string
	store  *sessionstore.Store
	lock   *sessionstore.Lock
	stored *sessionstore.Session
	client *expense.Client
}

func beginNamedSessionExecution(name string) (*namedSessionExecution, error) {
	store, err := sessionstore.DefaultStore()
	if err != nil {
		return nil, err
	}
	lock, err := store.AcquireLock(name)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*namedSessionExecution, error) {
		_ = lock.Release()
		return nil, err
	}
	stored, err := store.Load(name)
	if err != nil {
		return fail(err)
	}
	if stored.Status != sessionstore.StatusReady {
		return fail(fmt.Errorf("session %q is %s; re-import a fresh HAR", name, stored.Status))
	}
	profile, err := stored.Bootstrap()
	if err != nil {
		return fail(err)
	}
	client, err := expense.NewFromBootstrap(profile)
	if err != nil {
		return fail(err)
	}
	stored.Status = sessionstore.StatusInProgress
	if err := store.Save(name, stored); err != nil {
		return fail(err)
	}
	return &namedSessionExecution{name: name, store: store, lock: lock, stored: stored, client: client}, nil
}

func (execution *namedSessionExecution) finish(operationErr error) error {
	if execution == nil {
		return errors.New("session execution is nil")
	}
	var checkpointErr error
	profile, err := execution.client.SnapshotBootstrapProfile()
	if err != nil {
		checkpointErr = fmt.Errorf("snapshot session progress: %w", err)
	} else if err := execution.stored.ApplyBootstrap(profile); err != nil {
		checkpointErr = fmt.Errorf("apply session progress: %w", err)
	}
	if operationErr == nil && checkpointErr == nil {
		execution.stored.Status = sessionstore.StatusReady
	} else if errors.Is(operationErr, expense.ErrAuthenticationExpired) {
		execution.stored.Status = sessionstore.StatusExpired
	} else {
		execution.stored.Status = sessionstore.StatusUncertain
	}
	if err := execution.store.Save(execution.name, execution.stored); err != nil && checkpointErr == nil {
		checkpointErr = fmt.Errorf("persist session progress: %w", err)
	}
	if err := execution.lock.Release(); err != nil && checkpointErr == nil {
		checkpointErr = err
	}
	return checkpointErr
}
