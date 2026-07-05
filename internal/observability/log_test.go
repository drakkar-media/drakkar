package observability

import "testing"

func TestRecoverWithCleanupRunsCleanupOnPanic(t *testing.T) {
	cleanupRan := false
	var recoveredValue any

	func() {
		defer RecoverWithCleanup("test-goroutine", func(recovered any) {
			cleanupRan = true
			recoveredValue = recovered
		})
		panic("boom")
	}()

	if !cleanupRan {
		t.Fatal("expected cleanup to run after a panic")
	}
	if recoveredValue != "boom" {
		t.Fatalf("expected cleanup to receive the recovered value, got %v", recoveredValue)
	}
}

func TestRecoverWithCleanupSkipsCleanupWithoutPanic(t *testing.T) {
	cleanupRan := false

	func() {
		defer RecoverWithCleanup("test-goroutine", func(recovered any) {
			cleanupRan = true
		})
	}()

	if cleanupRan {
		t.Fatal("expected cleanup to be skipped when there was no panic")
	}
}
