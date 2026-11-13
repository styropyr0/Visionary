//go:build !linux

package main

// lowPriority does nothing here; the scan's pacing still limits it.
func lowPriority() {}
