package workstations

import "testing"

func TestHappyPathTransitions(t *testing.T) {
	path := []State{
		StateCreating, StatePullingImages, StateCreatingStorage,
		StateStartingVPN, StateWaitingForVPN, StateStartingApps, StateReady,
		StateStopping, StateStopped, StateStartingVPN, StateWaitingForVPN,
		StateStartingApps, StateReady, StateDeleting, StateDeleted,
	}
	for index := 1; index < len(path); index++ {
		if err := ValidateTransition(path[index-1], path[index]); err != nil {
			t.Fatalf("transition %s -> %s failed: %v", path[index-1], path[index], err)
		}
	}
}

func TestDeletedIsTerminal(t *testing.T) {
	if CanTransition(StateDeleted, StateReady) {
		t.Fatal("deleted workstation transitioned to ready")
	}
}

func TestInvalidTransition(t *testing.T) {
	if ValidateTransition(StateCreating, StateReady) == nil {
		t.Fatal("creating workstation skipped directly to ready")
	}
}

func TestReadyCanEnterControlledUpdate(t *testing.T) {
	if err := ValidateTransition(StateReady, StatePullingImages); err != nil {
		t.Fatal(err)
	}
}
