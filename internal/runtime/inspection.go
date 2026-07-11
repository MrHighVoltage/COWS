package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrInvalidObservation = errors.New("runtime: invalid observation")

type Inspection struct {
	RuntimeName  string
	Capabilities Capabilities
	Workspaces   []ObservedWorkspace
	ObservedAt   time.Time
}

// Inspect performs the read-only portion of reconciliation. It deliberately
// does not mutate the runtime or the database; the caller decides how observed
// state should be persisted and compared with desired state.
func Inspect(ctx context.Context, adapter Runtime) (Inspection, error) {
	if adapter == nil {
		return Inspection{}, ErrUnavailable
	}
	name, err := adapter.Name(ctx)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect runtime name: %w", err)
	}
	capabilities, err := adapter.Capabilities(ctx)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect runtime capabilities: %w", err)
	}
	workspaces, err := adapter.ListManaged(ctx)
	if err != nil {
		return Inspection{}, fmt.Errorf("list managed workspaces: %w", err)
	}
	if err := validateObservations(workspaces); err != nil {
		return Inspection{}, err
	}
	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].WorkspaceID == workspaces[j].WorkspaceID {
			return workspaces[i].RuntimeID < workspaces[j].RuntimeID
		}
		return workspaces[i].WorkspaceID < workspaces[j].WorkspaceID
	})
	return Inspection{RuntimeName: name, Capabilities: capabilities, Workspaces: workspaces, ObservedAt: time.Now().UTC()}, nil
}

func validateObservations(workspaces []ObservedWorkspace) error {
	workspaceIDs := make(map[string]struct{}, len(workspaces))
	runtimeIDs := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == "" || workspace.RuntimeID == "" {
			return fmt.Errorf("%w: managed workspace IDs must not be empty", ErrInvalidObservation)
		}
		if _, exists := workspaceIDs[workspace.WorkspaceID]; exists {
			return fmt.Errorf("%w: duplicate COWS workspace ID %q", ErrInvalidObservation, workspace.WorkspaceID)
		}
		if _, exists := runtimeIDs[workspace.RuntimeID]; exists {
			return fmt.Errorf("%w: duplicate runtime ID %q", ErrInvalidObservation, workspace.RuntimeID)
		}
		workspaceIDs[workspace.WorkspaceID] = struct{}{}
		runtimeIDs[workspace.RuntimeID] = struct{}{}
	}
	return nil
}
