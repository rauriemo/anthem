package workspace

import (
	"sync"
	"testing"
)

func TestFileLockConcurrent(t *testing.T) {
	fl := NewFileLock()
	counter := 0
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fl.Lock("shared.txt")
			counter++
			fl.Unlock("shared.txt")
		}()
	}
	wg.Wait()

	if counter != 100 {
		t.Errorf("counter = %d, want 100 (race detected)", counter)
	}
}

func TestFileLockDifferentPaths(t *testing.T) {
	fl := NewFileLock()
	done := make(chan struct{}, 2)

	fl.Lock("a.txt")
	go func() {
		fl.Lock("b.txt")
		fl.Unlock("b.txt")
		done <- struct{}{}
	}()

	<-done
	fl.Unlock("a.txt")
}

func TestValidatePathAcceptsSubpath(t *testing.T) {
	if err := ValidatePath("/workspace", "/workspace/project/file.go"); err != nil {
		t.Errorf("expected nil for valid subpath, got %v", err)
	}
}

func TestValidatePathRejectsTraversal(t *testing.T) {
	err := ValidatePath("/workspace", "/workspace/../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestValidatePathAcceptsSameDir(t *testing.T) {
	if err := ValidatePath("/workspace", "/workspace"); err != nil {
		t.Errorf("expected nil for same dir, got %v", err)
	}
}
