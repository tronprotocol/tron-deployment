package cmd

import (
	"reflect"
	"testing"
)

func TestStartUsesWriteLockedResolverByDefault(t *testing.T) {
	if got, want := reflect.ValueOf(resolveStartNodeContext).Pointer(), reflect.ValueOf(resolveNodeContextForWrite).Pointer(); got != want {
		t.Fatalf("start resolver = %x, want write-locked resolver %x", got, want)
	}
}
