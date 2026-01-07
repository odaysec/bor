// Package trie provides a wrapper around the triedb-go bindings
// that integrates with Bor's StateAccount type.
package trie

import (
	"errors"
	"fmt"

	"github.com/cffls/triedb-go/triedb-go"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/holiman/uint256"
)

// Re-export types and functions from triedb package
type (
	Database      = triedb.Database
	TransactionRO = triedb.TransactionRO
	TransactionRW = triedb.TransactionRW
	Address       = triedb.Address
	Hash          = triedb.Hash
	Witness       = triedb.Witness
	WitnessNode   = triedb.WitnessNode
)

// Re-export functions
var (
	Open           = triedb.Open
	CreateNew      = triedb.CreateNew
	AddressFromHex = triedb.AddressFromHex
	HashFromHex    = triedb.HashFromHex
)

// Re-export errors
var (
	ErrInvalidPath        = triedb.ErrInvalidPath
	ErrInvalidAddress     = triedb.ErrInvalidAddress
	ErrDatabaseOpenFailed = triedb.ErrDatabaseOpenFailed
	ErrTransactionFailed  = triedb.ErrTransactionFailed
	ErrNullPointer        = triedb.ErrNullPointer
	ErrUtf8Error          = triedb.ErrUtf8Error
	ErrAccountNotFound    = triedb.ErrAccountNotFound
	ErrStorageNotFound    = triedb.ErrStorageNotFound
)

// Conversion functions between triedb.Account and types.StateAccount

// ToStateAccount converts a triedb.Account to a types.StateAccount
func ToStateAccount(acc *triedb.Account) *types.StateAccount {
	if acc == nil {
		return nil
	}

	return &types.StateAccount{
		Nonce:    acc.Nonce,
		Balance:  acc.Balance,
		Root:     common.Hash(acc.StorageRoot),
		CodeHash: acc.CodeHash,
	}
}

// FromStateAccount converts a types.StateAccount to a triedb.Account
func FromStateAccount(acc *types.StateAccount) *triedb.Account {
	if acc == nil {
		return nil
	}

	return &triedb.Account{
		Nonce:       acc.Nonce,
		Balance:     acc.Balance,
		StorageRoot: triedb.Hash(acc.Root),
		CodeHash:    acc.CodeHash,
	}
}

// Extended transaction methods that work with StateAccount

// GetStateAccount retrieves a StateAccount from a read-only transaction
func GetStateAccount(tx *TransactionRO, address Address) (*types.StateAccount, error) {
	acc, err := tx.GetAccount(address)
	if err != nil {
		return nil, err
	}
	return ToStateAccount(acc), nil
}

// GetStateAccountRW retrieves a StateAccount from a read-write transaction
func GetStateAccountRW(tx *TransactionRW, address Address) (*types.StateAccount, error) {
	acc, err := tx.GetAccount(address)
	if err != nil {
		return nil, err
	}
	return ToStateAccount(acc), nil
}

// SetStateAccount sets a StateAccount in a read-write transaction
func SetStateAccount(tx *TransactionRW, address Address, account *types.StateAccount) error {
	return tx.SetAccount(address, FromStateAccount(account))
}

// TrieDB implements the Trie interface using triedb-go with overlay state
type TrieDB struct {
	db   *Database
	root common.Hash

	// Local cache of uncommitted changes (new changes since last commit)
	accounts map[Address]*types.StateAccount // nil means deleted
	storage  map[Address]map[Hash][]byte     // nil/empty means deleted

	// Buffer for accumulated committed changes (used for reads and overlay building)
	committedAccounts map[Address]*types.StateAccount
	committedStorage  map[Address]map[Hash][]byte

	// Track accounts and storage read from base state (for witness generation).
	// These are accounts/storage that were read but not modified during block execution.
	// They need to be included in the overlay so their trie nodes are part of the witness.
	readAccounts map[Address]*types.StateAccount
	readStorage  map[Address]map[Hash][]byte

	// Last computed root (for optimization when no new changes)
	lastComputedRoot common.Hash

	// Last generated witness (set by CommitWithWitness)
	lastWitness *Witness
}

// NewTrieDB creates a new Trie implementation using triedb-go
func NewTrieDB(root common.Hash, db *Database) (*TrieDB, error) {
	r, err := db.StateRoot()
	if err != nil {
		return nil, err
	}
	if common.Hash(r) != root && root != types.EmptyRootHash {
		return nil, fmt.Errorf("root mismatch: expected %s, got %s", root, common.Hash(r))
	}

	return &TrieDB{
		db:                db,
		root:              root,
		accounts:          make(map[Address]*types.StateAccount),
		storage:           make(map[Address]map[Hash][]byte),
		committedAccounts: make(map[Address]*types.StateAccount),
		committedStorage:  make(map[Address]map[Hash][]byte),
		readAccounts:      make(map[Address]*types.StateAccount),
		readStorage:       make(map[Address]map[Hash][]byte),
		lastComputedRoot:  root,
		lastWitness:       nil,
	}, nil
}

// Copy creates a deep copy of the TrieDB with its own accounts and storage maps.
// The underlying database is shared, but pending changes are isolated.
func (t *TrieDB) Copy() *TrieDB {
	// Deep copy accounts
	accounts := make(map[Address]*types.StateAccount, len(t.accounts))
	for addr, acc := range t.accounts {
		if acc != nil {
			// Deep copy the account
			accCopy := *acc
			accounts[addr] = &accCopy
		} else {
			accounts[addr] = nil
		}
	}

	// Deep copy storage
	storage := make(map[Address]map[Hash][]byte, len(t.storage))
	for addr, slots := range t.storage {
		if slots != nil {
			slotsCopy := make(map[Hash][]byte, len(slots))
			for slot, value := range slots {
				if value != nil {
					valueCopy := make([]byte, len(value))
					copy(valueCopy, value)
					slotsCopy[slot] = valueCopy
				} else {
					slotsCopy[slot] = nil
				}
			}
			storage[addr] = slotsCopy
		}
	}

	// Deep copy committedAccounts
	committedAccounts := make(map[Address]*types.StateAccount, len(t.committedAccounts))
	for addr, acc := range t.committedAccounts {
		if acc != nil {
			accCopy := *acc
			committedAccounts[addr] = &accCopy
		} else {
			committedAccounts[addr] = nil
		}
	}

	// Deep copy committedStorage
	committedStorage := make(map[Address]map[Hash][]byte, len(t.committedStorage))
	for addr, slots := range t.committedStorage {
		if slots != nil {
			slotsCopy := make(map[Hash][]byte, len(slots))
			for slot, value := range slots {
				if value != nil {
					valueCopy := make([]byte, len(value))
					copy(valueCopy, value)
					slotsCopy[slot] = valueCopy
				} else {
					slotsCopy[slot] = nil
				}
			}
			committedStorage[addr] = slotsCopy
		}
	}

	// Deep copy readAccounts
	readAccounts := make(map[Address]*types.StateAccount, len(t.readAccounts))
	for addr, acc := range t.readAccounts {
		if acc != nil {
			accCopy := *acc
			readAccounts[addr] = &accCopy
		} else {
			readAccounts[addr] = nil
		}
	}

	// Deep copy readStorage
	readStorage := make(map[Address]map[Hash][]byte, len(t.readStorage))
	for addr, slots := range t.readStorage {
		if slots != nil {
			slotsCopy := make(map[Hash][]byte, len(slots))
			for slot, value := range slots {
				if value != nil {
					valueCopy := make([]byte, len(value))
					copy(valueCopy, value)
					slotsCopy[slot] = valueCopy
				} else {
					slotsCopy[slot] = nil
				}
			}
			readStorage[addr] = slotsCopy
		}
	}

	return &TrieDB{
		db:                t.db,
		root:              t.root,
		accounts:          accounts,
		storage:           storage,
		committedAccounts: committedAccounts,
		committedStorage:  committedStorage,
		readAccounts:      readAccounts,
		readStorage:       readStorage,
		lastComputedRoot:  t.lastComputedRoot,
		lastWitness:       nil, // Witness is not copied, must be regenerated
	}
}

// GetKey returns the sha3 preimage of a hashed key
// TODO: Placeholder - not yet implemented
func (t *TrieDB) GetKey(key []byte) []byte {
	// Placeholder implementation
	return nil
}

// GetAccount retrieves an account from the trie
func (t *TrieDB) GetAccount(address common.Address) (*types.StateAccount, error) {
	addr := Address(address)

	// Check uncommitted changes first
	if acc, exists := t.accounts[addr]; exists {
		// nil means deleted or non-existent, return nil without error
		return acc, nil
	}

	// Check committed buffer next
	if acc, exists := t.committedAccounts[addr]; exists {
		// nil means deleted or non-existent, return nil without error
		return acc, nil
	}

	// Check if we already read this account from base state
	if acc, exists := t.readAccounts[addr]; exists {
		// nil means non-existent, return nil without error
		return acc, nil
	}

	// Read from base state using a temporary transaction
	tx, err := t.db.BeginRO()
	if err != nil {
		return nil, err
	}
	defer tx.Commit()

	acc, err := tx.GetAccount(addr)
	if err != nil {
		// Account not found is not an error - it means the account doesn't exist
		if err == ErrAccountNotFound {
			t.readAccounts[addr] = nil
			return nil, nil
		}
		return nil, err
	}

	// Track account read for witness generation
	stateAcc := ToStateAccount(acc)
	t.readAccounts[addr] = stateAcc

	return stateAcc, nil
}

// GetStorage retrieves a storage value from the trie
func (t *TrieDB) GetStorage(addr common.Address, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("storage key must be 32 bytes, got %d", len(key))
	}

	var slot Hash
	copy(slot[:], key)
	address := Address(addr)

	// Check uncommitted changes first
	if addrStorage, exists := t.storage[address]; exists {
		if value, exists := addrStorage[slot]; exists {
			if len(value) == 0 {
				// Storage was deleted
				return nil, nil
			}
			return value, nil
		}
	}

	// Check committed buffer next
	if addrStorage, exists := t.committedStorage[address]; exists {
		if value, exists := addrStorage[slot]; exists {
			if len(value) == 0 {
				// Storage was deleted
				return nil, nil
			}
			return value, nil
		}
	}

	// Check if we already read this storage slot from base state
	if addrStorage, exists := t.readStorage[address]; exists {
		if value, exists := addrStorage[slot]; exists {
			if len(value) == 0 {
				return nil, nil
			}
			return value, nil
		}
	}

	// Read from base state using a temporary transaction
	tx, err := t.db.BeginRO()
	if err != nil {
		return nil, err
	}
	defer tx.Commit()

	value, err := tx.GetStorage(address, slot)
	if err != nil {
		// Storage not found is not an error - it means the slot doesn't exist
		if err == ErrStorageNotFound {
			if t.readStorage[address] == nil {
				t.readStorage[address] = make(map[Hash][]byte)
			}
			t.readStorage[address][slot] = nil
			return nil, nil
		}
		return nil, err
	}

	// Track storage read for witness generation
	if t.readStorage[address] == nil {
		t.readStorage[address] = make(map[Hash][]byte)
	}
	if value == nil {
		t.readStorage[address][slot] = nil
		return nil, nil
	}
	// Store a copy of the value
	valueCopy := make([]byte, len(*value))
	copy(valueCopy, (*value)[:])
	t.readStorage[address][slot] = valueCopy

	return valueCopy, nil
}

// UpdateAccount updates an account in the trie
func (t *TrieDB) UpdateAccount(address common.Address, account *types.StateAccount, codeLen int) error {
	addr := Address(address)

	// Update local cache
	t.accounts[addr] = account

	return nil
}

// UpdateStorage updates a storage value in the trie
func (t *TrieDB) UpdateStorage(addr common.Address, key, value []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("storage key must be 32 bytes, got %d", len(key))
	}

	var slot Hash
	copy(slot[:], key)
	address := Address(addr)

	// Initialize storage map for this address if needed
	if t.storage[address] == nil {
		t.storage[address] = make(map[Hash][]byte)
	}

	if len(value) == 0 {
		// Delete storage
		t.storage[address][slot] = nil
		return nil
	}

	// Validate value
	if len(value) > 32 {
		return fmt.Errorf("storage value must be at most 32 bytes, got %d", len(value))
	}

	// Store in cache (keep original value for reads)
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)
	t.storage[address][slot] = valueCopy

	return nil
}

// DeleteAccount deletes an account from the trie
func (t *TrieDB) DeleteAccount(address common.Address) error {
	addr := Address(address)

	// Mark as deleted in cache
	t.accounts[addr] = nil

	return nil
}

// DeleteStorage deletes a storage value from the trie
func (t *TrieDB) DeleteStorage(addr common.Address, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("storage key must be 32 bytes, got %d", len(key))
	}

	var slot Hash
	copy(slot[:], key)
	address := Address(addr)

	// Initialize storage map for this address if needed
	if t.storage[address] == nil {
		t.storage[address] = make(map[Hash][]byte)
	}

	// Mark as deleted in cache
	t.storage[address][slot] = nil

	return nil
}

// UpdateContractCode stores contract code
// Note: This is a placeholder implementation as triedb-go may handle code differently
func (t *TrieDB) UpdateContractCode(address common.Address, codeHash common.Hash, code []byte) error {
	// TODO: Implement proper code storage mechanism
	// For now, this is a placeholder that doesn't actually store the code
	// The code hash should already be part of the account state
	return nil
}

// buildOverlay creates an overlay state from the committed changes buffer
// and includes read-only accounts/storage for witness generation.
func (t *TrieDB) buildOverlay() (*triedb.OverlayState, error) {
	overlay, err := triedb.NewOverlayState()
	if err != nil {
		return nil, err
	}

	// Insert all account changes from committed buffer
	for addr, acc := range t.committedAccounts {
		if err := overlay.InsertAccount(addr, FromStateAccount(acc)); err != nil {
			overlay.Close()
			return nil, err
		}
	}

	// Insert all storage changes from committed buffer
	for addr, slots := range t.committedStorage {
		for slot, value := range slots {
			if len(value) == 0 {
				// Deletion
				if err := overlay.InsertStorage(addr, slot, nil); err != nil {
					overlay.Close()
					return nil, err
				}
			} else {
				// Update - convert value to uint256.Int
				var valBytes [32]byte
				if len(value) > 32 {
					overlay.Close()
					return nil, fmt.Errorf("storage value too large: %d bytes", len(value))
				}
				copy(valBytes[32-len(value):], value)
				valInt := new(uint256.Int)
				valInt.SetBytes(valBytes[:])

				if err := overlay.InsertStorage(addr, slot, valInt); err != nil {
					overlay.Close()
					return nil, err
				}
			}
		}
	}

	// Insert read-only accounts (accounts read from base state but not modified).
	// These are included so their trie nodes appear in the witness.
	for addr, acc := range t.readAccounts {
		// Skip if this account was already in committed buffer (it was modified)
		if _, exists := t.committedAccounts[addr]; exists {
			continue
		}
		if err := overlay.InsertAccount(addr, FromStateAccount(acc)); err != nil {
			overlay.Close()
			return nil, err
		}
	}

	// Insert read-only storage (storage slots read from base state but not modified).
	// These are included so their trie nodes appear in the witness.
	for addr, slots := range t.readStorage {
		for slot, value := range slots {
			// Skip if this storage slot was already in committed buffer (it was modified)
			if addrStorage, exists := t.committedStorage[addr]; exists {
				if _, exists := addrStorage[slot]; exists {
					continue
				}
			}
			if len(value) == 0 {
				if err := overlay.InsertStorage(addr, slot, nil); err != nil {
					overlay.Close()
					return nil, err
				}
			} else {
				var valBytes [32]byte
				if len(value) > 32 {
					overlay.Close()
					return nil, fmt.Errorf("storage value too large: %d bytes", len(value))
				}
				copy(valBytes[32-len(value):], value)
				valInt := new(uint256.Int)
				valInt.SetBytes(valBytes[:])

				if err := overlay.InsertStorage(addr, slot, valInt); err != nil {
					overlay.Close()
					return nil, err
				}
			}
		}
	}

	return overlay, nil
}

// Hash returns the root hash of the trie
func (t *TrieDB) Hash() common.Hash {
	h, _ := t.Commit(false)
	return h
}

// Commit computes the new state root by applying the overlay changes
// and generates a witness containing all trie nodes accessed during the computation.
// Note: This does NOT persist changes to the database, it only computes the new root.
func (t *TrieDB) Commit(collectLeaf bool) (common.Hash, *trienode.NodeSet) {
	root, _, _ := t.CommitWithWitness(collectLeaf)
	return root, nil
}

// CommitWithWitness computes the new state root and generates a witness
// containing all trie nodes accessed during the computation.
// This is used for stateless verification of state transitions.
func (t *TrieDB) CommitWithWitness(collectLeaf bool) (common.Hash, *Witness, *trienode.NodeSet) {
	// Close any previous witness
	if t.lastWitness != nil {
		t.lastWitness.Close()
		t.lastWitness = nil
	}

	// Check if we have any work to do (changes or reads to include in witness)
	hasChanges := len(t.accounts) > 0 || len(t.storage) > 0
	hasReads := len(t.readAccounts) > 0 || len(t.readStorage) > 0

	// Optimization: if no changes and no reads since last commit, return cached root
	if !hasChanges && !hasReads && len(t.committedAccounts) == 0 && len(t.committedStorage) == 0 {
		return t.lastComputedRoot, nil, nil
	}

	// Merge uncommitted changes into committed buffers
	for addr, acc := range t.accounts {
		t.committedAccounts[addr] = acc
	}
	for addr, slots := range t.storage {
		if t.committedStorage[addr] == nil {
			t.committedStorage[addr] = make(map[Hash][]byte)
		}
		for slot, value := range slots {
			t.committedStorage[addr][slot] = value
		}
	}

	// Clear uncommitted changes
	t.accounts = make(map[Address]*types.StateAccount)
	t.storage = make(map[Address]map[Hash][]byte)

	// Build overlay from committed buffers (includes read-only accounts/storage)
	overlay, err := t.buildOverlay()
	if err != nil {
		log.Error("Failed to build overlay", "err", err)
		return t.root, nil, nil
	}
	defer overlay.Close()

	tx, err := t.db.BeginRO()
	if err != nil {
		log.Error("Failed to begin read-only transaction", "err", err)
		return t.root, nil, nil
	}
	defer tx.Commit()

	root, witness, err := tx.ComputeRootWithOverlayAndWitness(overlay)
	if err != nil {
		log.Error("Failed to compute root with overlay and witness", "err", err)
		return t.root, nil, nil
	}

	// Cache the computed root and witness
	t.lastComputedRoot = common.Hash(root)
	t.lastWitness = witness

	// Clear read tracking for the next block
	t.readAccounts = make(map[Address]*types.StateAccount)
	t.readStorage = make(map[Address]map[Hash][]byte)

	return t.lastComputedRoot, witness, nil
}

// Witness returns the witness from the last CommitWithWitness call.
// Returns nil if CommitWithWitness has not been called or if there were no changes.
// The returned map contains RLP-encoded trie nodes as keys (matching the standard trie format).
func (t *TrieDB) Witness() map[string]struct{} {
	if t.lastWitness == nil {
		return nil
	}

	nodes, err := t.lastWitness.Nodes()
	if err != nil {
		log.Error("Failed to get witness nodes", "err", err)
		return nil
	}

	// Convert to the expected format: RLP-encoded node data as keys
	// This matches the standard trie.Witness() format used by stateless.Witness.AddState()
	result := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		result[string(node.Data)] = struct{}{}
	}
	return result
}

// GetWitness returns the raw witness from the last CommitWithWitness call.
// This provides direct access to the witness data including node RLP.
func (t *TrieDB) GetWitness() *Witness {
	return t.lastWitness
}

// GetWitnessNodes returns all witness nodes from the last CommitWithWitness call.
// Each node contains its hash and RLP-encoded data.
func (t *TrieDB) GetWitnessNodes() ([]WitnessNode, error) {
	if t.lastWitness == nil {
		return nil, nil
	}
	return t.lastWitness.Nodes()
}

// SerializeWitness serializes the witness for transmission or storage.
func (t *TrieDB) SerializeWitness() ([]byte, error) {
	if t.lastWitness == nil {
		return nil, nil
	}
	return t.lastWitness.Serialize()
}

// ClearWitness releases the witness resources.
func (t *TrieDB) ClearWitness() {
	if t.lastWitness != nil {
		t.lastWitness.Close()
		t.lastWitness = nil
	}
}

// NodeIterator returns an iterator for trie nodes
// TODO: Placeholder - not yet implemented
func (t *TrieDB) NodeIterator(startKey []byte) (NodeIterator, error) {
	// Placeholder implementation
	return nil, errors.New("NodeIterator not yet implemented")
}

// Prove generates a Merkle proof for a key
// TODO: Placeholder - not yet implemented
func (t *TrieDB) Prove(key []byte, proofDb ethdb.KeyValueWriter) error {
	// Placeholder implementation
	return errors.New("Prove not yet implemented")
}

// IsVerkle returns false as this is not a Verkle trie
func (t *TrieDB) IsVerkle() bool {
	return false
}

// MergeReadsFrom merges the read tracking data from another TrieDB instance.
// This is used to combine reads from the reader path with the commit path
// for complete witness generation. Only accounts/storage that were read
// (not modified) in the other trie are merged.
func (t *TrieDB) MergeReadsFrom(other *TrieDB) {
	if other == nil {
		return
	}

	// Merge readAccounts from other trie
	for addr, acc := range other.readAccounts {
		// Skip if already tracked in this trie (either as read, committed, or uncommitted)
		if _, exists := t.readAccounts[addr]; exists {
			continue
		}
		if _, exists := t.committedAccounts[addr]; exists {
			continue
		}
		if _, exists := t.accounts[addr]; exists {
			continue
		}
		// Copy the account (deep copy if not nil)
		if acc != nil {
			accCopy := *acc
			t.readAccounts[addr] = &accCopy
		} else {
			t.readAccounts[addr] = nil
		}
	}

	// Merge readStorage from other trie
	for addr, slots := range other.readStorage {
		if slots == nil {
			continue
		}
		for slot, value := range slots {
			// Skip if already tracked in this trie
			if addrStorage, exists := t.readStorage[addr]; exists {
				if _, exists := addrStorage[slot]; exists {
					continue
				}
			}
			if addrStorage, exists := t.committedStorage[addr]; exists {
				if _, exists := addrStorage[slot]; exists {
					continue
				}
			}
			if addrStorage, exists := t.storage[addr]; exists {
				if _, exists := addrStorage[slot]; exists {
					continue
				}
			}

			// Initialize storage map for this address if needed
			if t.readStorage[addr] == nil {
				t.readStorage[addr] = make(map[Hash][]byte)
			}

			// Copy the value (deep copy if not nil)
			if value != nil {
				valueCopy := make([]byte, len(value))
				copy(valueCopy, value)
				t.readStorage[addr][slot] = valueCopy
			} else {
				t.readStorage[addr][slot] = nil
			}
		}
	}
}
