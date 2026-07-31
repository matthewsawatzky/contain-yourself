// Package workstations owns workstation state transitions and persistence.
package workstations

import "fmt"

type State string

const (
	StateCreating        State = "creating"
	StatePullingImages   State = "pulling-images"
	StateCreatingStorage State = "creating-storage"
	StateStartingVPN     State = "starting-vpn"
	StateWaitingForVPN   State = "waiting-for-vpn"
	StateStartingApps    State = "starting-apps"
	StateReady           State = "ready"
	StateVPNFailed       State = "vpn-failed"
	StateUnhealthy       State = "unhealthy"
	StateLocked          State = "locked"
	StateStopping        State = "stopping"
	StateStopped         State = "stopped"
	StateDeleting        State = "deleting"
	StateDeleted         State = "deleted"
	StateError           State = "error"
)

var transitions = map[State]map[State]bool{
	StateCreating:        {StatePullingImages: true, StateCreatingStorage: true, StateError: true, StateDeleting: true},
	StatePullingImages:   {StateCreatingStorage: true, StateError: true, StateDeleting: true},
	StateCreatingStorage: {StateStartingVPN: true, StateStartingApps: true, StateError: true, StateDeleting: true},
	StateStartingVPN:     {StateWaitingForVPN: true, StateVPNFailed: true, StateError: true, StateDeleting: true},
	StateWaitingForVPN:   {StateStartingApps: true, StateVPNFailed: true, StateLocked: true, StateError: true, StateDeleting: true},
	StateStartingApps:    {StateReady: true, StateUnhealthy: true, StateVPNFailed: true, StateError: true, StateDeleting: true},
	StateReady:           {StatePullingImages: true, StateStopping: true, StateUnhealthy: true, StateVPNFailed: true, StateLocked: true, StateDeleting: true, StateError: true},
	StateVPNFailed:       {StatePullingImages: true, StateStartingVPN: true, StateLocked: true, StateStopping: true, StateDeleting: true, StateError: true},
	StateUnhealthy:       {StatePullingImages: true, StateStartingApps: true, StateStopping: true, StateDeleting: true, StateError: true},
	StateLocked:          {StatePullingImages: true, StateStartingVPN: true, StateStopping: true, StateDeleting: true, StateError: true},
	StateStopping:        {StateStopped: true, StateError: true},
	StateStopped:         {StatePullingImages: true, StateCreatingStorage: true, StateStartingVPN: true, StateStartingApps: true, StateDeleting: true, StateError: true},
	StateDeleting:        {StateDeleted: true, StateError: true},
	StateError:           {StatePullingImages: true, StateCreatingStorage: true, StateStartingVPN: true, StateStartingApps: true, StateStopping: true, StateDeleting: true},
	StateDeleted:         {},
}

func CanTransition(from, to State) bool {
	return transitions[from][to]
}

func ValidateTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid workstation state transition %q -> %q", from, to)
	}
	return nil
}
