package store_test

import (
	"testing"

	"summermahjong/riichi/internal/store"
	"summermahjong/riichi/internal/store/storetest"
)

// TestMemoryStore 让内存实现跑契约套件（sql / redis 实现也用同一套）。
func TestMemoryStore(t *testing.T) {
	storetest.Run(t, func() store.Store { return store.NewMemory() })
}
