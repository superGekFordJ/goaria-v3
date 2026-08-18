package extension

import "testing"

func TestSecretStore_Generation_FirstSetBumps(t *testing.T) {
	store := NewSecretStore()
	if store.Generation() != 0 {
		t.Fatalf("new store generation want 0, got %d", store.Generation())
	}
	store.SetSecret("alpha")
	if store.Generation() != 1 {
		t.Fatalf("first set want generation 1, got %d", store.Generation())
	}
	if store.GetSecret() != "alpha" {
		t.Fatalf("secret not stored: %q", store.GetSecret())
	}
}

func TestSecretStore_Generation_SameValueNoBump(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("alpha")
	store.SetSecret("alpha")
	if store.Generation() != 1 {
		t.Fatalf("same-value set must not bump, got %d", store.Generation())
	}
}

func TestSecretStore_Generation_ChangeBumpsAgain(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("alpha")
	store.SetSecret("beta")
	if store.Generation() != 2 {
		t.Fatalf("changed secret want generation 2, got %d", store.Generation())
	}
	store.SetSecret("")
	if store.Generation() != 3 {
		t.Fatalf("empty change want generation 3, got %d", store.Generation())
	}
}
