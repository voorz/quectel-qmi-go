package qmi

import "bytes"

// SlotStatusDiff describes what changed between two slot status snapshots.
type SlotStatusDiff struct {
	// ActiveSlotChanged is true if the active slot (physical slot with ACTIVE status) changed.
	ActiveSlotChanged bool
	// ChangedSlots contains 0-based indices of slots whose status changed.
	ChangedSlots []int
}

// DiffSlotStatus compares old and new slot status arrays.
// If activeSlotOnly is true, only changes to the active slot are considered significant.
// Otherwise, all slot changes are reported.
//
// This mirrors ModemManager's slot_array_status_equal() + slot_status_equal() logic:
// - Active slot change → requires modem reprobe
// - Inactive slot change → only requires SIM object update
func DiffSlotStatus(old, new []UIMSlotStatusSlot, activeSlotOnly bool) SlotStatusDiff {
	diff := SlotStatusDiff{}

	maxLen := len(old)
	if len(new) > maxLen {
		maxLen = len(new)
	}

	for i := 0; i < maxLen; i++ {
		var oldSlot, newSlot UIMSlotStatusSlot
		if i < len(old) {
			oldSlot = old[i]
		}
		if i < len(new) {
			newSlot = new[i]
		}

		if !slotStatusEqual(oldSlot, newSlot) {
			// Check if this is an active slot change
			wasActive := oldSlot.PhysicalSlotStatus == UIMSlotStateActive
			nowActive := newSlot.PhysicalSlotStatus == UIMSlotStateActive
			if wasActive || nowActive {
				diff.ActiveSlotChanged = true
			}

			if !activeSlotOnly {
				diff.ChangedSlots = append(diff.ChangedSlots, i)
			} else if wasActive || nowActive {
				diff.ChangedSlots = append(diff.ChangedSlots, i)
			}
		}
	}

	return diff
}

// slotStatusEqual returns true if two slot statuses are equivalent.
// This mirrors ModemManager's slot_status_equal().
func slotStatusEqual(a, b UIMSlotStatusSlot) bool {
	return a.PhysicalCardStatus == b.PhysicalCardStatus &&
		a.PhysicalSlotStatus == b.PhysicalSlotStatus &&
		a.LogicalSlot == b.LogicalSlot &&
		a.ICCID == b.ICCID
}

// SlotStatusEqual is the exported version of slotStatusEqual for testing.
func SlotStatusEqual(a, b UIMSlotStatusSlot) bool {
	return slotStatusEqual(a, b)
}

// ActiveSlotIndexChanged returns true if the active slot's logical slot
// mapping changed (meaning the active physical slot is now mapped to a
// different logical slot).
func ActiveSlotIndexChanged(old, new []UIMSlotStatusSlot) bool {
	oldActive := findActiveSlot(old)
	newActive := findActiveSlot(new)

	if oldActive == nil && newActive == nil {
		return false // no active slot in either
	}
	if oldActive == nil || newActive == nil {
		return true // active slot appeared or disappeared
	}
	return oldActive.LogicalSlot != newActive.LogicalSlot
}

// findActiveSlot returns the first active slot, or nil if none.
func findActiveSlot(slots []UIMSlotStatusSlot) *UIMSlotStatusSlot {
	for i := range slots {
		if slots[i].PhysicalSlotStatus == UIMSlotStateActive {
			return &slots[i]
		}
	}
	return nil
}

// ICCIDChanged returns true if the ICCID of any slot changed between old and new.
func ICCIDChanged(old, new []UIMSlotStatusSlot) bool {
	maxLen := len(old)
	if len(new) > maxLen {
		maxLen = len(new)
	}
	for i := 0; i < maxLen; i++ {
		var oldICCID, newICCID string
		if i < len(old) {
			oldICCID = old[i].ICCID
		}
		if i < len(new) {
			newICCID = new[i].ICCID
		}
		if oldICCID != newICCID {
			return true
		}
	}
	return false
}

// CardPresenceChanged returns true if a card was inserted or removed from any slot.
func CardPresenceChanged(old, new []UIMSlotStatusSlot) bool {
	maxLen := len(old)
	if len(new) > maxLen {
		maxLen = len(new)
	}
	for i := 0; i < maxLen; i++ {
		var oldPresent, newPresent bool
		if i < len(old) {
			oldPresent = old[i].PhysicalCardStatus == UIMPhysicalCardStatePresent
		}
		if i < len(new) {
			newPresent = new[i].PhysicalCardStatus == UIMPhysicalCardStatePresent
		}
		if oldPresent != newPresent {
			return true
		}
	}
	return false
}

// EIDChanged returns true if the EID of any slot changed (eUICC identity change).
func EIDChanged(old, new []UIMSlotStatusSlot) bool {
	maxLen := len(old)
	if len(new) > maxLen {
		maxLen = len(new)
	}
	for i := 0; i < maxLen; i++ {
		var oldEID, newEID []byte
		if i < len(old) {
			oldEID = old[i].EID
		}
		if i < len(new) {
			newEID = new[i].EID
		}
		if !bytes.Equal(oldEID, newEID) {
			return true
		}
	}
	return false
}
