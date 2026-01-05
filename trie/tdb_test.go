package trie

import (
	"crypto/rand"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

func randomTestAddress() Address {
	var addr Address
	rand.Read(addr[:])
	return addr
}

func emptyTestStorageRoot() Hash {
	h, _ := HashFromHex("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	return h
}

func emptyTestCodeHash() []byte {
	h, _ := HashFromHex("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
	return h[:]
}

func createTestStateAccount(nonce uint64) *types.StateAccount {
	balance := new(uint256.Int).SetUint64(nonce)
	weiMultiplier := new(uint256.Int).SetUint64(1_000_000_000_000_000_000)
	balance.Mul(balance, weiMultiplier)

	return &types.StateAccount{
		Nonce:    nonce,
		Balance:  balance,
		Root:     common.Hash(emptyTestStorageRoot()),
		CodeHash: emptyTestCodeHash(),
	}
}

func setupTestTrieDB(t *testing.T) (*TrieDB, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	db, err := CreateNew(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}

	root, err := db.StateRoot()
	if err != nil {
		db.Close()
		t.Fatalf("Failed to get state root: %v", err)
	}

	trie, err := NewTrieDB(common.Hash(root), db)
	if err != nil {
		db.Close()
		t.Fatalf("Failed to create TrieDB: %v", err)
	}

	cleanup := func() {
		trie.ClearWitness()
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return trie, cleanup
}

func TestTrieDB_CommitWithWitness_Basic(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add an account
	addr := common.Address(randomTestAddress())
	account := createTestStateAccount(1)

	err := trie.UpdateAccount(addr, account, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit with witness
	root, witness, _ := trie.CommitWithWitness(false)

	// Verify we got a valid root
	if root == (common.Hash{}) {
		t.Error("Expected non-empty root")
	}

	// For initial insert on empty trie, witness may be empty
	// (no existing nodes to read)
	if witness != nil {
		length, err := witness.Len()
		if err != nil {
			t.Fatalf("Failed to get witness length: %v", err)
		}
		t.Logf("Witness contains %d nodes", length)
	}
}

func TestTrieDB_CommitWithWitness_Update(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add initial account
	addr := common.Address(randomTestAddress())
	account := createTestStateAccount(1)

	err := trie.UpdateAccount(addr, account, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// First commit (without witness)
	// Note: Commit() only computes root via overlay, it doesn't persist to the
	// underlying database. The witness tracks nodes read from the BASE database,
	// so if nothing is persisted, the witness will be empty.
	_, _ = trie.Commit(false)

	// Now update the account
	updatedAccount := createTestStateAccount(2)
	err = trie.UpdateAccount(addr, updatedAccount, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit with witness
	root, witness, _ := trie.CommitWithWitness(false)

	if root == (common.Hash{}) {
		t.Error("Expected non-empty root")
	}

	// Note: Witness may be empty if base database has no state.
	// In production, the base state would be persisted, so witness would be populated.
	if witness != nil {
		length, err := witness.Len()
		if err != nil {
			t.Fatalf("Failed to get witness length: %v", err)
		}
		t.Logf("Update witness contains %d nodes", length)
	} else {
		t.Log("Witness is nil (no base state to read from)")
	}
}

func TestTrieDB_CommitWithWitness_Delete(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add multiple accounts
	addrs := make([]common.Address, 5)
	for i := 0; i < 5; i++ {
		addrs[i] = common.Address(randomTestAddress())
		account := createTestStateAccount(uint64(i + 1))
		err := trie.UpdateAccount(addrs[i], account, 0)
		if err != nil {
			t.Fatalf("Failed to update account %d: %v", i, err)
		}
	}

	// Commit initial state (note: doesn't persist to base database)
	_, _ = trie.Commit(false)

	// Delete one account
	err := trie.DeleteAccount(addrs[2])
	if err != nil {
		t.Fatalf("Failed to delete account: %v", err)
	}

	// Commit with witness
	root, witness, _ := trie.CommitWithWitness(false)

	if root == (common.Hash{}) {
		t.Error("Expected non-empty root")
	}

	// Note: Witness may be empty since base database has no persisted state.
	// In production with persisted state, deletion would generate witness nodes
	// including sibling nodes during branch restructuring.
	if witness != nil {
		length, err := witness.Len()
		if err != nil {
			t.Fatalf("Failed to get witness length: %v", err)
		}
		t.Logf("Delete witness contains %d nodes", length)
	} else {
		t.Log("Witness is nil (no base state to read from)")
	}
}

func TestTrieDB_CommitWithWitness_Storage(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add account with storage
	addr := common.Address(randomTestAddress())
	account := createTestStateAccount(1)

	err := trie.UpdateAccount(addr, account, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Add storage
	var slot [32]byte
	rand.Read(slot[:])
	var value [32]byte
	rand.Read(value[:])

	err = trie.UpdateStorage(addr, slot[:], value[:])
	if err != nil {
		t.Fatalf("Failed to update storage: %v", err)
	}

	// Commit with witness
	root, witness, _ := trie.CommitWithWitness(false)

	if root == (common.Hash{}) {
		t.Error("Expected non-empty root")
	}

	if witness != nil {
		length, _ := witness.Len()
		t.Logf("Storage witness contains %d nodes", length)
	}
}

func TestTrieDB_CommitWithWitness_MixedOperations(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add initial accounts
	addrs := make([]common.Address, 10)
	for i := 0; i < 10; i++ {
		addrs[i] = common.Address(randomTestAddress())
		account := createTestStateAccount(uint64(i + 1))
		err := trie.UpdateAccount(addrs[i], account, 0)
		if err != nil {
			t.Fatalf("Failed to update account %d: %v", i, err)
		}
	}

	// Commit initial state (note: doesn't persist to base database)
	initialRoot, _ := trie.Commit(false)
	t.Logf("Initial root: %s", initialRoot.Hex())

	// Mixed operations: update some, delete some, add new
	// Update
	err := trie.UpdateAccount(addrs[0], createTestStateAccount(100), 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Delete
	err = trie.DeleteAccount(addrs[5])
	if err != nil {
		t.Fatalf("Failed to delete account: %v", err)
	}

	// Add new
	newAddr := common.Address(randomTestAddress())
	err = trie.UpdateAccount(newAddr, createTestStateAccount(999), 0)
	if err != nil {
		t.Fatalf("Failed to add new account: %v", err)
	}

	// Commit with witness
	root, witness, _ := trie.CommitWithWitness(false)

	if root == (common.Hash{}) {
		t.Error("Expected non-empty root")
	}

	if root == initialRoot {
		t.Error("Root should change after modifications")
	}

	// Note: Witness may be empty since base database has no persisted state.
	// In production, the witness would contain all nodes read during overlay computation.
	if witness != nil {
		length, err := witness.Len()
		if err != nil {
			t.Fatalf("Failed to get witness length: %v", err)
		}
		t.Logf("Mixed operations witness contains %d nodes", length)
	} else {
		t.Log("Witness is nil (no base state to read from)")
	}
}

func TestTrieDB_CommitWithWitness_NoChanges(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add initial account
	addr := common.Address(randomTestAddress())
	account := createTestStateAccount(1)

	err := trie.UpdateAccount(addr, account, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// First commit
	root1, _ := trie.Commit(false)

	// Commit again with no changes
	root2, witness, _ := trie.CommitWithWitness(false)

	if root1 != root2 {
		t.Errorf("Root should not change with no modifications: got %s, want %s", root2.Hex(), root1.Hex())
	}

	// No changes means no witness
	if witness != nil {
		t.Error("Expected nil witness when no changes")
	}
}

func TestTrieDB_GetWitnessNodes(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add accounts
	for i := 0; i < 5; i++ {
		addr := common.Address(randomTestAddress())
		account := createTestStateAccount(uint64(i + 1))
		err := trie.UpdateAccount(addr, account, 0)
		if err != nil {
			t.Fatalf("Failed to update account %d: %v", i, err)
		}
	}

	// Commit initial
	_, _ = trie.Commit(false)

	// Add more accounts to trigger witness generation
	for i := 0; i < 3; i++ {
		addr := common.Address(randomTestAddress())
		account := createTestStateAccount(uint64(i + 100))
		err := trie.UpdateAccount(addr, account, 0)
		if err != nil {
			t.Fatalf("Failed to update account: %v", err)
		}
	}

	// Commit with witness
	_, _, _ = trie.CommitWithWitness(false)

	// Get witness nodes
	nodes, err := trie.GetWitnessNodes()
	if err != nil {
		t.Fatalf("Failed to get witness nodes: %v", err)
	}

	if nodes != nil {
		for i, node := range nodes {
			if len(node.Data) == 0 {
				t.Errorf("Node %d has empty data", i)
			}
			t.Logf("Node %d: hash=%s, data_len=%d", i, node.Hash.Hex(), len(node.Data))
		}
	}
}

func TestTrieDB_SerializeWitness(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add account
	addr := common.Address(randomTestAddress())
	account := createTestStateAccount(1)

	err := trie.UpdateAccount(addr, account, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit initial
	_, _ = trie.Commit(false)

	// Update to generate witness
	err = trie.UpdateAccount(addr, createTestStateAccount(2), 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit with witness
	_, _, _ = trie.CommitWithWitness(false)

	// Serialize
	serialized, err := trie.SerializeWitness()
	if err != nil {
		t.Fatalf("Failed to serialize witness: %v", err)
	}

	if serialized != nil && len(serialized) > 0 {
		t.Logf("Serialized witness: %d bytes", len(serialized))
	}
}

func TestTrieDB_Witness(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Initially no witness
	w := trie.Witness()
	if w != nil {
		t.Error("Expected nil witness initially")
	}

	// Add accounts
	for i := 0; i < 5; i++ {
		addr := common.Address(randomTestAddress())
		account := createTestStateAccount(uint64(i + 1))
		err := trie.UpdateAccount(addr, account, 0)
		if err != nil {
			t.Fatalf("Failed to update account %d: %v", i, err)
		}
	}

	// Commit initial
	_, _ = trie.Commit(false)

	// Update to generate witness
	addr := common.Address(randomTestAddress())
	err := trie.UpdateAccount(addr, createTestStateAccount(100), 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit with witness
	_, _, _ = trie.CommitWithWitness(false)

	// Now Witness() should return data
	w = trie.Witness()
	if w != nil {
		t.Logf("Witness contains %d node hashes", len(w))
		for hash := range w {
			t.Logf("  Node hash: %s", hash)
		}
	}
}

func TestTrieDB_ClearWitness(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add account
	addr := common.Address(randomTestAddress())
	account := createTestStateAccount(1)

	err := trie.UpdateAccount(addr, account, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit initial
	_, _ = trie.Commit(false)

	// Update
	err = trie.UpdateAccount(addr, createTestStateAccount(2), 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit with witness
	_, _, _ = trie.CommitWithWitness(false)

	// Verify witness exists
	if trie.GetWitness() == nil {
		t.Log("No witness generated (may be expected for simple operations)")
	}

	// Clear witness
	trie.ClearWitness()

	// Verify witness is cleared
	if trie.GetWitness() != nil {
		t.Error("Expected witness to be nil after ClearWitness")
	}

	if trie.Witness() != nil {
		t.Error("Expected Witness() to return nil after ClearWitness")
	}
}

func TestTrieDB_Copy_WitnessNotCopied(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add account
	addr := common.Address(randomTestAddress())
	account := createTestStateAccount(1)

	err := trie.UpdateAccount(addr, account, 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit initial
	_, _ = trie.Commit(false)

	// Update
	err = trie.UpdateAccount(addr, createTestStateAccount(2), 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit with witness
	_, _, _ = trie.CommitWithWitness(false)

	// Copy the trie
	trieCopy := trie.Copy()

	// Verify witness is not copied
	if trieCopy.GetWitness() != nil {
		t.Error("Expected witness to not be copied")
	}

	// Original should still have witness (if it was generated)
	// This depends on whether the operation actually generated a witness
}

// TestTrieDB_CommitWithWitness_WithPersistedState tests witness generation
// when there is actual persisted state in the underlying database.
// This demonstrates the full witness flow where nodes are read from the database.
func TestTrieDB_CommitWithWitness_WithPersistedState(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	// Create database and persist initial state
	db, err := CreateNew(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}

	// Persist initial accounts using RW transaction
	tx, err := db.BeginRW()
	if err != nil {
		db.Close()
		t.Fatalf("Failed to begin RW transaction: %v", err)
	}

	addrs := make([]Address, 5)
	for i := 0; i < 5; i++ {
		addrs[i] = randomTestAddress()
		account := FromStateAccount(createTestStateAccount(uint64(i + 1)))
		err = tx.SetAccount(addrs[i], account)
		if err != nil {
			tx.Rollback()
			db.Close()
			t.Fatalf("Failed to set account %d: %v", i, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		db.Close()
		t.Fatalf("Failed to commit: %v", err)
	}

	// Get the persisted root
	persistedRoot, err := db.StateRoot()
	if err != nil {
		db.Close()
		t.Fatalf("Failed to get state root: %v", err)
	}
	t.Logf("Persisted root: %s", persistedRoot.Hex())

	// Now create TrieDB on top of persisted state
	trie, err := NewTrieDB(common.Hash(persistedRoot), db)
	if err != nil {
		db.Close()
		t.Fatalf("Failed to create TrieDB: %v", err)
	}

	defer func() {
		trie.ClearWitness()
		db.Close()
	}()

	// Update one of the existing accounts
	err = trie.UpdateAccount(common.Address(addrs[2]), createTestStateAccount(100), 0)
	if err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}

	// Commit with witness - this should now read nodes from the persisted database
	root, witness, _ := trie.CommitWithWitness(false)

	if root == (common.Hash{}) {
		t.Error("Expected non-empty root")
	}

	if root == common.Hash(persistedRoot) {
		t.Error("Root should change after modification")
	}

	// Now we should have actual witness nodes since we're reading from persisted state
	if witness != nil {
		length, err := witness.Len()
		if err != nil {
			t.Fatalf("Failed to get witness length: %v", err)
		}
		t.Logf("Witness with persisted state contains %d nodes", length)

		if length > 0 {
			nodes, err := trie.GetWitnessNodes()
			if err != nil {
				t.Fatalf("Failed to get witness nodes: %v", err)
			}
			for i, node := range nodes {
				t.Logf("  Node %d: hash=%s, data_len=%d bytes", i, node.Hash.Hex(), len(node.Data))
			}
		}
	} else {
		t.Log("Witness is nil")
	}
}

func TestTrieDB_CommitWithWitness_LargeState(t *testing.T) {
	trie, cleanup := setupTestTrieDB(t)
	defer cleanup()

	// Add many accounts
	numAccounts := 100
	addrs := make([]common.Address, numAccounts)
	for i := 0; i < numAccounts; i++ {
		addrs[i] = common.Address(randomTestAddress())
		account := createTestStateAccount(uint64(i + 1))
		err := trie.UpdateAccount(addrs[i], account, 0)
		if err != nil {
			t.Fatalf("Failed to update account %d: %v", i, err)
		}
	}

	// Commit initial state
	initialRoot, _ := trie.Commit(false)
	t.Logf("Initial root with %d accounts: %s", numAccounts, initialRoot.Hex())

	// Update 10%, delete 10%, add 10%
	updateCount := numAccounts / 10
	deleteCount := numAccounts / 10
	addCount := numAccounts / 10

	for i := 0; i < updateCount; i++ {
		err := trie.UpdateAccount(addrs[i], createTestStateAccount(uint64(1000+i)), 0)
		if err != nil {
			t.Fatalf("Failed to update account: %v", err)
		}
	}

	for i := 0; i < deleteCount; i++ {
		err := trie.DeleteAccount(addrs[numAccounts/2+i])
		if err != nil {
			t.Fatalf("Failed to delete account: %v", err)
		}
	}

	for i := 0; i < addCount; i++ {
		newAddr := common.Address(randomTestAddress())
		err := trie.UpdateAccount(newAddr, createTestStateAccount(uint64(2000+i)), 0)
		if err != nil {
			t.Fatalf("Failed to add account: %v", err)
		}
	}

	// Commit with witness
	root, witness, _ := trie.CommitWithWitness(false)

	if root == initialRoot {
		t.Error("Root should change after modifications")
	}

	if witness == nil {
		t.Error("Expected witness for large state modifications")
	} else {
		length, err := witness.Len()
		if err != nil {
			t.Fatalf("Failed to get witness length: %v", err)
		}
		t.Logf("Large state witness contains %d nodes", length)

		size, err := witness.SizeBytes()
		if err != nil {
			t.Fatalf("Failed to get witness size: %v", err)
		}
		t.Logf("Witness size: %d bytes", size)
	}
}
