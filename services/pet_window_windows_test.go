//go:build windows

package services

import "testing"

func TestPetWindowApplyIgnoreMouseStylePreservesWindowSafetyBits(t *testing.T) {
	base := uint32(petWindowWSExNoActivate | petWindowWSExLayered)

	interactive := petWindowApplyIgnoreMouseStyle(base|uint32(petWindowWSExTransparent), false)
	if petWindowHasIgnoreMouseStyle(interactive) {
		t.Fatalf("interactive style = %#x, want WS_EX_TRANSPARENT cleared", interactive)
	}
	if interactive&uint32(petWindowWSExLayered) == 0 || interactive&uint32(petWindowWSExNoActivate) == 0 {
		t.Fatalf("interactive style = %#x, layered/noactivate safety bits were lost", interactive)
	}

	passive := petWindowApplyIgnoreMouseStyle(interactive, true)
	if !petWindowHasIgnoreMouseStyle(passive) {
		t.Fatalf("passive style = %#x, want WS_EX_TRANSPARENT set", passive)
	}
	if passive&uint32(petWindowWSExLayered) == 0 || passive&uint32(petWindowWSExNoActivate) == 0 {
		t.Fatalf("passive style = %#x, layered/noactivate safety bits were lost", passive)
	}
}

func TestPetWindowApplyIgnoreMouseStyleIsIdempotent(t *testing.T) {
	styles := []struct {
		name   string
		style  uint32
		ignore bool
	}{
		{name: "passive", style: uint32(petWindowWSExNoActivate | petWindowWSExLayered | petWindowWSExTransparent), ignore: true},
		{name: "interactive", style: uint32(petWindowWSExNoActivate | petWindowWSExLayered), ignore: false},
	}

	for _, item := range styles {
		t.Run(item.name, func(t *testing.T) {
			updated := petWindowApplyIgnoreMouseStyle(item.style, item.ignore)
			if updated != item.style {
				t.Fatalf("style %#x changed to %#x for already-applied ignore=%t", item.style, updated, item.ignore)
			}
		})
	}
}
