package session

// keyRegistry maps friendly key names to the raw bytes injected into a session.
// This is the exact, closed MVP set.
var keyRegistry = map[string][]byte{
	"enter":  {0x0D}, // carriage return
	"ctrl-c": {0x03},
	"escape": {0x1B},
}

// keyNames lists the valid key names in a stable order for error messages.
var keyNamesOrder = []string{"enter", "ctrl-c", "escape"}

// keyBytes returns the bytes for a key name and whether it is registered.
func keyBytes(name string) ([]byte, bool) {
	b, ok := keyRegistry[name]
	return b, ok
}

// keyNames returns the registered key names in a stable order.
func keyNames() []string {
	return keyNamesOrder
}
