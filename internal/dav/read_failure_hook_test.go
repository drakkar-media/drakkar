package dav

import (
	"context"
	"errors"
	"testing"
)

func TestReportReadFailureOnlyForMissingArticles(t *testing.T) {
	var got []int64
	SetReadFailureHook(func(id int64, _ error) { got = append(got, id) })
	t.Cleanup(func() { readFailureHook.Store(nil) })

	reportReadFailure(1, errors.New("fetch decoded article x@y: article not found (cached): x@y"))
	reportReadFailure(2, errors.New("Newshosting attempt 1: unexpected BODY status 430"))
	reportReadFailure(3, context.Canceled)
	reportReadFailure(4, errors.New("fetch decoded article x@y: i/o timeout"))
	reportReadFailure(0, errors.New("article not found"))

	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("hook calls = %v, want [1 2]", got)
	}
}
